// Package owner controls the tape from users
package owner

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrorOwnerNotFound = errors.New("ownerId not found")

const (
	SystemUser  = "user"
	SystemAgent = "agent"
	UserPrefix  = "u:"
)

func UserID(id string) string {
	return UserPrefix + strings.TrimPrefix(id, UserPrefix)
}

func IsUserID(id string) bool {
	return strings.HasPrefix(id, UserPrefix)
}

type ownerIdKey struct{}

// WithOwnerId injects current userId into the context
func WithOwnerId(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ownerIdKey{}, id)
}

func GetOwnerId(ctx context.Context) (string, error) {
	owner, ok := ctx.Value(ownerIdKey{}).(string)
	if !ok {
		return "", fmt.Errorf("owner: %w", ErrorOwnerNotFound)
	}
	return owner, nil
}
