package slurm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

type accountJSON struct {
	Errors   []json.RawMessage `json:"errors"`
	Accounts []struct {
		Name         string            `json:"name"`
		Description  string            `json:"description"`
		Organization string            `json:"organization"`
		Coordinators []json.RawMessage `json:"coordinators"`
		Associations []json.RawMessage `json:"associations"`
	} `json:"accounts"`
}

type userJSON struct {
	Errors []json.RawMessage `json:"errors"`
	Users  []struct {
		Name               string   `json:"name"`
		AdministratorLevel []string `json:"administrator_level"`
		Default            struct {
			Account string `json:"account"`
			WCKey   string `json:"wckey"`
		} `json:"default"`
		Associations []json.RawMessage `json:"associations"`
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
	directory, err := c.accountCache.get(parent, func(ctx context.Context) (cluster.AccountDirectory, error) {
		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		accountsOutput, err := c.run(ctx, "sacctmgr", "--json", "show", "account", "WithAssoc")
		if err != nil {
			return cluster.AccountDirectory{}, fmt.Errorf("read Slurm accounts: %w", err)
		}
		accounts, err := parseAccountsJSON(accountsOutput)
		if err != nil {
			return cluster.AccountDirectory{}, fmt.Errorf("parse Slurm accounts: %w", err)
		}
		usersOutput, err := c.run(ctx, "sacctmgr", "--json", "show", "user", "WithAssoc")
		if err != nil {
			return cluster.AccountDirectory{}, fmt.Errorf("read Slurm users: %w", err)
		}
		users, err := parseUsersJSON(usersOutput)
		if err != nil {
			return cluster.AccountDirectory{}, fmt.Errorf("parse Slurm users: %w", err)
		}
		return cluster.AccountDirectory{Accounts: accounts, Users: users}, nil
	})
	directory.Accounts = append([]cluster.Account(nil), directory.Accounts...)
	directory.Users = append([]cluster.SlurmUser(nil), directory.Users...)
	return directory, err
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
		result = append(result, cluster.Account{Name: value.Name, Description: value.Description, Organization: value.Organization, CoordinatorCount: len(value.Coordinators), AssociationCount: len(value.Associations)})
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
		result = append(result, cluster.SlurmUser{Name: value.Name, AdministratorLevel: adminLevel, DefaultAccount: value.Default.Account, DefaultWCKey: value.Default.WCKey, AssociationCount: len(value.Associations)})
	}
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
