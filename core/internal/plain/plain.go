// Package plain turns the errors Go and the network produce into the sentences people read:
// what happened and what to do next. The raw error stays available for logs and "details".
package plain

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"strings"
	"syscall"
)

// Unreached says why a computer or service did not answer, without the layers Go's network
// errors come wrapped in (`Post "http://127.0.0.1:53012/v1/runs": dial tcp ...: connectex: ...`).
func Unreached(err error) string {
	var timeout interface{ Timeout() bool }
	var dns *net.DNSError
	var op *net.OpError
	var req *url.Error
	switch {
	case errors.As(err, &dns):
		return "the address " + dns.Name + " was not found"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return "it did not answer in time"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Addr != nil {
			return "nothing answers at " + op.Addr.String()
		}
		return "nothing answers at its address"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "the connection dropped"
	case errors.As(err, &req):
		return req.Err.Error()
	}
	return err.Error()
}

// certificate reports a TLS certificate the client does not accept.
func certificate(err error) bool {
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	return errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid) || errors.As(err, &verify)
}

// Provider is one sentence about a provider that failed its health check: label is how Settings
// names it. The raw text is for the caller to keep as the detail.
func Provider(label string, err error) string {
	var status interface{ StatusCode() int }
	switch {
	case err == nil:
		return ""
	case certificate(err):
		return label + " answered with a certificate that is not the one it was paired with. Remove it and add it again if it was reinstalled."
	case errors.As(err, &status):
		switch code := status.StatusCode(); {
		case code == 401 || code == 403:
			return label + " refused the access token. Add it again with a new pairing code."
		case code == 404:
			return label + " answered, but not as a Forja engine. Check its address."
		case code >= 500:
			return label + " reported an error of its own. Try again in a moment."
		}
	}
	text := Unreached(err)
	if text == err.Error() {
		return label + " did not respond as expected. Check that it is running and connected."
	}
	return label + " could not be reached: " + text + ". Check that it is running and connected."
}

// File says what went wrong with a file or folder, for the person who named it.
func File(err error) string {
	var pe *fs.PathError
	name := ""
	if errors.As(err, &pe) {
		name = pe.Path
	}
	subject := "that file or folder"
	if name != "" {
		subject = name
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return subject + " was not found. Check the path."
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES):
		return "Forja is not allowed to use " + subject + ". Check its permissions, or choose another place."
	case errors.Is(err, syscall.ENOSPC):
		return "The disk is full, so " + subject + " could not be written. Free some space and try again."
	}
	return strings.TrimSpace(fmt.Sprint(err))
}

// Err is err as a sentence for a person when it is one of the common file or connection
// failures, and its own text otherwise (most errors the core makes already say what to do).
func Err(err error) string {
	if err == nil {
		return ""
	}
	var pe *fs.PathError
	var sys syscall.Errno
	switch {
	case errors.As(err, &pe) || errors.As(err, &sys):
		if text := File(err); text != err.Error() {
			return text
		}
	case certificate(err):
		return "The certificate the other side showed is not trusted. If it was reinstalled, remove it and add it again."
	}
	if text := Unreached(err); text != err.Error() {
		return "Could not connect: " + text + ". Check your connection and the address."
	}
	return err.Error()
}
