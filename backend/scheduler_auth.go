package main

// This marker is attached only to in-process scheduler requests. HTTP headers,
// cookies and forwarded client addresses cannot create a Go context value.
type internalTaskAuthKey struct{}
