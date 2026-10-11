package hvserver

import "context"

// sweep ends the VMs a service before this one left behind, as a crash of
// it may have: their clients are gone, and the ledger, new with this
// service, holds none of their memory. list lists the ids of the
// service's VMs and end ends one; a VM that won't end is logged, and the
// rest ended all the same.
func sweep(ctx context.Context, list func(context.Context) ([]string, error), end func(context.Context, string) error, logf func(format string, args ...any)) {
	ids, err := list(ctx)
	if err != nil {
		logf("list the VMs left behind: %v", err)

		return
	}

	for _, id := range ids {
		if err := end(ctx, id); err != nil {
			logf("end VM %s, left behind: %v", id, err)

			continue
		}

		logf("ended VM %s, left behind", id)
	}
}
