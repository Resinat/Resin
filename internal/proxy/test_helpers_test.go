package proxy

import (
	"context"
	"net"
)

// Test error types for classifyUpstreamError tests.

type deadlineExceededErr struct{}

func (deadlineExceededErr) Error() string   { return "deadline exceeded" }
func (deadlineExceededErr) Timeout() bool   { return true }
func (deadlineExceededErr) Temporary() bool { return true }
func (deadlineExceededErr) Unwrap() error   { return context.DeadlineExceeded }

type canceledErr struct{}

func (canceledErr) Error() string { return "context canceled" }
func (canceledErr) Unwrap() error { return context.Canceled }

// dialErr simulates a net.OpError with Op="dial".
type dialErr struct{}

func (dialErr) Error() string { return "dial tcp: connection refused" }
func (dialErr) Unwrap() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host"}}
}

type genericErr struct{}

func (genericErr) Error() string { return "some generic error" }
