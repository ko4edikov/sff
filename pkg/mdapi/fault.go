package mdapi

import (
	"encoding/xml"
	"strings"
)

// soapFault represents a SOAP fault returned by the Metadata API.
type soapFault struct {
	Code   string
	String string
}

func (f *soapFault) Error() string {
	if f.Code != "" {
		return "metadata API fault: " + f.Code + ": " + f.String
	}
	return "metadata API fault: " + f.String
}

// isInvalidSession reports whether the fault is an expired/invalid session,
// which is recoverable via a token refresh.
func (f *soapFault) isInvalidSession() bool {
	return strings.Contains(f.Code, "INVALID_SESSION_ID") ||
		strings.Contains(f.String, "INVALID_SESSION_ID")
}

// parseFault extracts a SOAP fault from a response body, or returns nil if the
// body is not a fault.
func parseFault(body []byte) *soapFault {
	if !strings.Contains(string(body), "faultcode") {
		return nil
	}
	var env struct {
		Fault struct {
			Code   string `xml:"faultcode"`
			String string `xml:"faultstring"`
		} `xml:"Body>Fault"`
	}
	if err := xml.Unmarshal(body, &env); err != nil {
		return &soapFault{String: snippet(body)}
	}
	if env.Fault.Code == "" && env.Fault.String == "" {
		return nil
	}
	return &soapFault{Code: env.Fault.Code, String: env.Fault.String}
}
