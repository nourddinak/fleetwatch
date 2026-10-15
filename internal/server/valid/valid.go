// Package valid holds the error type for user-correctable input problems.
// The API turns it into a 400 with the message shown to the user.
package valid

type Error string

func (e Error) Error() string { return string(e) }
