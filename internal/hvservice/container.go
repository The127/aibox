package hvservice

import "github.com/Microsoft/go-winio/pkg/guid"

// ContainerName is the name of the AppContainer of the id, which the
// service makes for the container alone and tells only the client: the
// SID of a container depends only on its name, so a name anyone could
// learn, such as that of the VM, would be a container others could start
// programs in too.
func ContainerName(id guid.GUID) string {
	return "aibox." + id.String()
}
