// Command module runs the light-painting module, registering the
// light-painting-controller generic service model.
package main

import (
	"light-painting/controller"
	"light-painting/scene"

	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/worldstatestore"
)

func main() {
	module.ModularMain(
		resource.APIModel{API: generic.API, Model: controller.Model},
		resource.APIModel{API: worldstatestore.API, Model: scene.Model},
	)
}
