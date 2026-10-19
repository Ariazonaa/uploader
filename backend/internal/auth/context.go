package auth

import (
	"context"

	"uploader/internal/db/store"
)

type ctxKey int

const (
	userKey ctxKey = iota
	methodKey
)

// Method records how a request was authenticated.
type Method int

const (
	// MethodSession is a browser login: the session cookie.
	MethodSession Method = iota + 1
	// MethodToken is an API token (Authorization: Bearer or X-Uploader-Token).
	MethodToken
)

// WithUser returns a child context carrying the authenticated user.
func WithUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFrom extracts the authenticated user from the context, if any.
func UserFrom(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(userKey).(*store.User)
	return u, ok
}

// MustUser returns the authenticated user or panics; use only behind Require.
func MustUser(ctx context.Context) *store.User {
	u, ok := UserFrom(ctx)
	if !ok {
		panic("auth: no user in context (handler not behind Require middleware)")
	}
	return u
}

// WithMethod returns a child context recording how the request authenticated.
func WithMethod(ctx context.Context, m Method) context.Context {
	return context.WithValue(ctx, methodKey, m)
}

// MethodFrom reports how the request authenticated; 0 when it did not.
func MethodFrom(ctx context.Context) Method {
	m, _ := ctx.Value(methodKey).(Method)
	return m
}
