package monthlyplaylist

import (
	"errors"
	"fmt"
)

type SessionFetchError struct {
	Err error
}

func (e SessionFetchError) Error() string {
	return fmt.Sprintf("couldn't fetch session: %s", e.Err)
}

type ExpectedFormValueError struct {
	Key string
}

func (e ExpectedFormValueError) Error() string {
	return fmt.Sprintf("expected form value for %s", e.Key)
}

type UserIDUnexpectedTypeError struct {
	ID interface{}
}

func (e UserIDUnexpectedTypeError) Error() string {
	return fmt.Sprintf("user ID found with unexpected type: %T", e.ID)
}

var (
	ErrNotLoggedIn         = errors.New("user not logged in")
	ErrUserIDNotSet        = errors.New("no user ID found in session")
	ErrStateNotSet         = errors.New("no state found in session")
	ErrStateUnexpectedType = errors.New("state found with unexpected type")
	ErrStateMismatch       = errors.New("state in query and session are different")
)
