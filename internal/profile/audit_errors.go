package profile

import "errors"

// ErrAuditSecretsDetected is returned by the CLI when `ccp profile audit`
// surfaces at least one finding that the caller wants to gate on (i.e.,
// when `ccp profile export --fail-on-audit` finds suspect content). Maps
// to ExitConflict: the command did its job, but there is state the user
// must address before proceeding.
//
// `ccp profile audit` by itself also returns this when findings exist —
// agents can branch on the nonzero exit without parsing stdout.
var ErrAuditSecretsDetected = errors.New("audit detected suspected secrets in profile")
