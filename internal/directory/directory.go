package directory

import (
	"context"
	"errors"
	"path"
	"unicode/utf8"
)

const (
	minPOSIXID       = 1000
	maxPOSIXID       = 2_147_483_647
	minUserPassword  = 12
	maxUserPassword  = 72
	maxUserNameBytes = 64
)

type User struct {
	UID           string
	Name          string
	Email         string
	UIDNumber     int64
	GIDNumber     int64
	HomeDirectory string
	LoginShell    string
}

type Group struct {
	Name             string
	Description      string
	GIDNumber        int64
	Members          []string
	MembersTruncated bool
}

type Page struct {
	Users     []User
	Groups    []Group
	Truncated bool
}

type Provider interface {
	Search(context.Context, string) (Page, error)
	User(context.Context, string) (User, bool, error)
	Group(context.Context, string) (Group, bool, error)
}

type UserCreateRequest struct {
	UID           string
	UIDNumber     int64
	GIDNumber     int64
	HomeDirectory string
	LoginShell    string
	Password      string
}

type UserCreator interface {
	CreateUser(context.Context, UserCreateRequest) error
}

// UserProvisioningStatus reports whether a directory provider is safely
// configured to create LDAP users.
type UserProvisioningStatus interface {
	UserProvisioningAvailable() bool
}

func ValidateUserCreateRequest(request UserCreateRequest) error {
	if !validPOSIXUsername(request.UID) {
		return errors.New("LDAP username is invalid")
	}
	if request.UIDNumber < minPOSIXID || request.UIDNumber > maxPOSIXID || request.GIDNumber < minPOSIXID || request.GIDNumber > maxPOSIXID {
		return errors.New("LDAP UID or GID is invalid")
	}
	if !validAbsolutePath(request.HomeDirectory) || !validAbsolutePath(request.LoginShell) {
		return errors.New("LDAP home directory or shell is invalid")
	}
	if len(request.Password) < minUserPassword || len([]byte(request.Password)) > maxUserPassword || !utf8.ValidString(request.Password) {
		return errors.New("LDAP password is invalid")
	}
	return nil
}

func validPOSIXUsername(value string) bool {
	if len(value) == 0 || len(value) > maxUserNameBytes {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	return value != "." && value != ".."
}

func validAbsolutePath(value string) bool {
	return len(value) > 1 && len(value) <= 1024 && utf8.ValidString(value) && path.IsAbs(value) && path.Clean(value) == value
}
