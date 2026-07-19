package web

import (
	"io"
	"os"
)

const jobOutputReaderArgument = "--openhpc-job-output-reader"

func HandleJobOutputReaderInvocation(arguments []string, output io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != jobOutputReaderArgument {
		return false, nil
	}
	return true, runJobOutputReader(arguments, os.Stdin, output)
}
