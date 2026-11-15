package ciimages

import (
	"os"
	"testing"

	"github.com/MichaelKinsy/PiG/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunScoped(m, "pig-ciimages-"))
}
