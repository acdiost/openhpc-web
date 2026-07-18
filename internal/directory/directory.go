package directory

import "context"

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
