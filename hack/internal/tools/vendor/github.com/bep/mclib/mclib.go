package mclib

import (
	"fmt"
	"os"
	"strings"

	"github.com/bep/mclib/internal"
)

var errorReplacer = strings.NewReplacer(
	"ERROR: ", "",
)

// RunMain runs mkcert's main function.
//
// You need to set os.Args before calling this function, e.g.
//
//	os.Args = []string{"-install"}
//	os.Args = []string{"-cert-file", "cert.pem", "-key-file", "key.pem", "example.com"}
func RunMain() (err error) {
	defer func() {
		if r := recover(); r != nil {
			errStr := fmt.Sprintf("%v", r)
			errStr = errorReplacer.Replace(errStr)
			err = fmt.Errorf(errStr)
		}
	}()

	const mkcert = "mkcert"
	if len(os.Args) == 0 || os.Args[0] != mkcert {
		// All commands needs the "mkcert" as its first argument.
		os.Args = append([]string{mkcert}, os.Args...)
	}
	internal.RunMain()
	return
}

// GetCAROOT returns the CA root directory.
func GetCAROOT() string {
	return internal.GetCAROOT()
}
