package reach

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"syscall"
)

// Windows socket error numbers. syscall.ECONNREFUSED and its siblings on
// Windows are not these values, so an error from the socket layer carries
// the raw number and is matched on it.
const (
	wsa_econnaborted = 10053
	wsa_econnreset   = 10054
	wsa_econnrefused = 10061
)

// classify puts the error of a failed tcp or tls step into the closed
// Failure set. An error it does not recognise is not left out of the set:
// at the tcp step it is a connection that never came up and reads as
// timeout; at the tls step it is a handshake that ended abnormally and reads
// as reset.
func classify(err error, handshake bool) Failure {
	if handshake && is_certificate_error(err) {
		return Failure_bad_certificate
	}
	if is_timeout(err) {
		return Failure_timeout
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNREFUSED, wsa_econnrefused:
			return Failure_refused
		case syscall.ECONNRESET, syscall.ECONNABORTED, wsa_econnreset, wsa_econnaborted:
			return Failure_reset
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return Failure_reset
	}
	if handshake {
		return Failure_reset
	}
	return Failure_timeout
}

// is_certificate_error is true for any way the server's certificate failed
// verification.
func is_certificate_error(err error) bool {
	var verification *tls.CertificateVerificationError
	var unknown_authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verification) ||
		errors.As(err, &unknown_authority) ||
		errors.As(err, &hostname) ||
		errors.As(err, &invalid)
}
