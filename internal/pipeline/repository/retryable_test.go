package repository

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
)

func TestIsRetryableErrorWithoutTemporary(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"temporary DNS", fmt.Errorf("lookup: %w", &net.DNSError{Err: "server misbehaving", IsTemporary: true}), true},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"connection aborted", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNABORTED}, true},
		{"permanent DNS", &net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{"plain error", errors.New("bad input"), false},
	}
	for _, tc := range cases {
		if got := isRetryableError(tc.err); got != tc.want {
			t.Errorf("%s: isRetryableError() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
