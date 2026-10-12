package hvservice

import (
	"strings"

	"github.com/Microsoft/go-winio/pkg/guid"
)

// containerPrefix starts the name of each container of the service.
const containerPrefix = "aibox."

// ContainerName is the name of the AppContainer of the id, which the
// service makes for the container alone and tells only the client: the
// SID of a container depends only on its name, so a name anyone could
// learn, such as that of the VM, would be a container others could start
// programs in too.
func ContainerName(id guid.GUID) string {
	return containerPrefix + id.String()
}

// IsContainerName is whether the name is one ContainerName gives, in any
// case, as Windows keeps the names of containers in lower case.
func IsContainerName(name string) bool {
	id, ok := strings.CutPrefix(strings.ToLower(name), containerPrefix)
	if !ok {
		return false
	}

	parsed, err := guid.FromString(id)

	return err == nil && parsed.String() == id
}
