package color_test

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func ExampleGreen() {
	color.SetEnabled(false)
	fmt.Println(color.Green("deployed %s", "v1.5.5"))
	// Output: deployed v1.5.5
}
