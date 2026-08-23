package sessionjsonl

import "path/filepath"

const ownerLockFileName = ".ingot-owner.lock"

func ownerLockPath(root string) string {
	return filepath.Join(root, ownerLockFileName)
}
