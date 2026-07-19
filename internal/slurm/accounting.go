package slurm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

type accountJSON struct {
	Errors   []json.RawMessage `json:"errors"`
	Accounts []struct {
		Name         string            `json:"name"`
		Description  string            `json:"description"`
		Organization string            `json:"organization"`
		Coordinators []json.RawMessage `json:"coordinators"`
		Associations associationCount  `json:"associations"`
	} `json:"accounts"`
}

type associationCount int

func (c *associationCount) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return errors.New("association list must be an array")
	}
	count := 0
	for decoder.More() {
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			return fmt.Errorf("decode association list item: %w", err)
		}
		count++
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("close association list: %w", err)
	}
	*c = associationCount(count)
	return nil
}

type associationJSON struct {
	ID        slurmNumber `json:"id"`
	Cluster   string      `json:"cluster"`
	Account   string      `json:"account"`
	User      string      `json:"user"`
	Partition string      `json:"partition"`
}

type accountingSnapshot struct {
	Directory      cluster.AccountDirectory
	Associations   []cluster.Association
	AssociationErr error
}

const maxAssociationRecords = 10_000

type userJSON struct {
	Errors []json.RawMessage `json:"errors"`
	Users  []struct {
		Name               string   `json:"name"`
		AdministratorLevel []string `json:"administrator_level"`
		Default            struct {
			Account string `json:"account"`
			WCKey   string `json:"wckey"`
		} `json:"default"`
		Associations associationCount `json:"associations"`
	} `json:"users"`
}

type slurmFloat struct {
	Set      bool    `json:"set"`
	Infinite bool    `json:"infinite"`
	Number   float64 `json:"number"`
}

type qosJSON struct {
	Errors []json.RawMessage `json:"errors"`
	QoS    []struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Priority    slurmNumber `json:"priority"`
		UsageFactor slurmFloat  `json:"usage_factor"`
		Limits      struct {
			Max struct {
				Jobs struct {
					Count slurmNumber `json:"count"`
				} `json:"jobs"`
			} `json:"max"`
		} `json:"limits"`
	} `json:"qos"`
}

func (c *Client) AccountDirectory(parent context.Context) (cluster.AccountDirectory, error) {
	snapshot, err := c.loadAccountingSnapshot(parent)
	directory := snapshot.Directory
	directory.Accounts = append([]cluster.Account(nil), directory.Accounts...)
	directory.Users = append([]cluster.SlurmUser(nil), directory.Users...)
	return directory, err
}

func (c *Client) Associations(parent context.Context) ([]cluster.Association, error) {
	snapshot, err := c.loadAccountingSnapshot(parent)
	if err != nil {
		return nil, err
	}
	return append([]cluster.Association(nil), snapshot.Associations...), snapshot.AssociationErr
}

func (c *Client) loadAccountingSnapshot(parent context.Context) (accountingSnapshot, error) {
	return c.accountCache.get(parent, func(ctx context.Context) (accountingSnapshot, error) {
		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		accountsOutput, err := c.run(ctx, "sacctmgr", "--json", "show", "account", "WithAssoc")
		if err != nil {
			return accountingSnapshot{}, fmt.Errorf("read Slurm accounts: %w", err)
		}
		accounts, err := parseAccountsJSON(accountsOutput)
		if err != nil {
			return accountingSnapshot{}, fmt.Errorf("parse Slurm accounts: %w", err)
		}
		usersOutput, err := c.run(ctx, "sacctmgr", "--json", "show", "user", "WithAssoc")
		if err != nil {
			return accountingSnapshot{}, fmt.Errorf("read Slurm users: %w", err)
		}
		users, err := parseUsersJSON(usersOutput)
		if err != nil {
			return accountingSnapshot{}, fmt.Errorf("parse Slurm users: %w", err)
		}
		associations, associationErr := parseAssociationsJSON(accountsOutput, usersOutput)
		return accountingSnapshot{
			Directory:    cluster.AccountDirectory{Accounts: accounts, Users: users},
			Associations: associations, AssociationErr: associationErr,
		}, nil
	})
}

func (c *Client) QoS(parent context.Context) ([]cluster.QoS, error) {
	qos, err := c.qosCache.get(parent, func(ctx context.Context) ([]cluster.QoS, error) {
		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		output, err := c.run(ctx, "sacctmgr", "--json", "show", "qos")
		if err != nil {
			return nil, fmt.Errorf("read Slurm QoS: %w", err)
		}
		return parseQoSJSON(output)
	})
	return append([]cluster.QoS(nil), qos...), err
}

func parseAccountsJSON(output []byte) ([]cluster.Account, error) {
	var response accountJSON
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode account JSON: %w", err)
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("account JSON reported %d errors", len(response.Errors))
	}
	result := make([]cluster.Account, 0, len(response.Accounts))
	for _, value := range response.Accounts {
		if value.Name == "" {
			return nil, errors.New("account name is required")
		}
		if err := validateDetailStrings([]string{value.Name, value.Description, value.Organization}); err != nil {
			return nil, err
		}
		result = append(result, cluster.Account{Name: value.Name, Description: value.Description, Organization: value.Organization, CoordinatorCount: len(value.Coordinators), AssociationCount: int(value.Associations)})
	}
	return result, nil
}

func parseUsersJSON(output []byte) ([]cluster.SlurmUser, error) {
	var response userJSON
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode user JSON: %w", err)
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("user JSON reported %d errors", len(response.Errors))
	}
	result := make([]cluster.SlurmUser, 0, len(response.Users))
	for _, value := range response.Users {
		if value.Name == "" {
			return nil, errors.New("user name is required")
		}
		adminLevel := strings.Join(value.AdministratorLevel, ", ")
		if err := validateDetailStrings([]string{value.Name, adminLevel, value.Default.Account, value.Default.WCKey}); err != nil {
			return nil, err
		}
		result = append(result, cluster.SlurmUser{Name: value.Name, AdministratorLevel: adminLevel, DefaultAccount: value.Default.Account, DefaultWCKey: value.Default.WCKey, AssociationCount: int(value.Associations)})
	}
	return result, nil
}

func parseAssociationsJSON(accountsOutput, usersOutput []byte) ([]cluster.Association, error) {
	if err := validateCombinedAssociationCount(accountsOutput, usersOutput); err != nil {
		return nil, err
	}
	var accounts struct {
		Accounts []struct {
			Associations []json.RawMessage `json:"associations"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(accountsOutput, &accounts); err != nil {
		return nil, fmt.Errorf("decode account associations JSON: %w", err)
	}
	var users struct {
		Users []struct {
			Associations []json.RawMessage `json:"associations"`
		} `json:"users"`
	}
	if err := json.Unmarshal(usersOutput, &users); err != nil {
		return nil, fmt.Errorf("decode user associations JSON: %w", err)
	}
	records := make([]json.RawMessage, 0)
	for _, account := range accounts.Accounts {
		records = append(records, account.Associations...)
	}
	for _, user := range users.Users {
		records = append(records, user.Associations...)
	}
	return parseAssociationRecords(records)
}

func validateCombinedAssociationCount(accountsOutput, usersOutput []byte) error {
	var accounts accountJSON
	if err := json.Unmarshal(accountsOutput, &accounts); err != nil {
		return fmt.Errorf("count account associations: %w", err)
	}
	var users userJSON
	if err := json.Unmarshal(usersOutput, &users); err != nil {
		return fmt.Errorf("count user associations: %w", err)
	}
	total := 0
	for _, account := range accounts.Accounts {
		total += int(account.Associations)
		if total > maxAssociationRecords {
			return errors.New("too many Slurm association records")
		}
	}
	for _, user := range users.Users {
		total += int(user.Associations)
		if total > maxAssociationRecords {
			return errors.New("too many Slurm association records")
		}
	}
	return nil
}

func parseAssociationRecords(records []json.RawMessage) ([]cluster.Association, error) {
	if len(records) > maxAssociationRecords {
		return nil, errors.New("too many Slurm association records")
	}
	byID := make(map[int64]cluster.Association, len(records))
	for _, record := range records {
		var value associationJSON
		if err := json.Unmarshal(record, &value); err != nil {
			return nil, fmt.Errorf("decode association JSON: %w", err)
		}
		if !value.ID.Set || value.ID.Infinite || value.ID.Number <= 0 || value.Cluster == "" || value.Account == "" {
			return nil, errors.New("association ID, cluster, and account are required")
		}
		if err := validateDetailStrings([]string{value.Cluster, value.Account, value.User, value.Partition}); err != nil {
			return nil, err
		}
		association := cluster.Association{
			ID: value.ID.Number, Cluster: value.Cluster, Account: value.Account,
			User: value.User, Partition: value.Partition,
		}
		if existing, found := byID[association.ID]; found && existing != association {
			return nil, fmt.Errorf("association ID %d has conflicting records", association.ID)
		}
		byID[association.ID] = association
	}
	result := make([]cluster.Association, 0, len(byID))
	for _, association := range byID {
		result = append(result, association)
	}
	sort.Slice(result, func(left, right int) bool {
		first, second := result[left], result[right]
		if first.Cluster != second.Cluster {
			return first.Cluster < second.Cluster
		}
		if first.Account != second.Account {
			return first.Account < second.Account
		}
		if first.User != second.User {
			return first.User < second.User
		}
		if first.Partition != second.Partition {
			return first.Partition < second.Partition
		}
		return first.ID < second.ID
	})
	return result, nil
}

func parseQoSJSON(output []byte) ([]cluster.QoS, error) {
	var response qosJSON
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode QoS JSON: %w", err)
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("QoS JSON reported %d errors", len(response.Errors))
	}
	result := make([]cluster.QoS, 0, len(response.QoS))
	for _, value := range response.QoS {
		if value.Name == "" {
			return nil, errors.New("QoS name is required")
		}
		if err := validateDetailStrings([]string{value.Name, value.Description}); err != nil {
			return nil, err
		}
		result = append(result, cluster.QoS{Name: value.Name, Description: value.Description, Priority: value.Priority.Number, UsageFactor: value.UsageFactor.Number, MaxJobs: value.Limits.Max.Jobs.Count.Number, MaxJobsUnlimited: value.Limits.Max.Jobs.Count.Infinite})
	}
	return result, nil
}
