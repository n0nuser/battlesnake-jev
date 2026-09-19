package jev

import "errors"

// asAPIError is a thin wrapper over errors.As kept separate so the retry loop
// reads cleanly.
func asAPIError(err error, target **APIError) bool {
	return errors.As(err, target)
}
