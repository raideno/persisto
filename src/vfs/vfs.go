package vfs

import (
	"persisto/src/utils"
	"persisto/src/vfs/localvfs"
	"persisto/src/vfs/memoryvfs"
	"persisto/src/vfs/remotevfs"
)

func RegisterVfs() error {
	utils.Logger.Info("Registering Memory VFS.")
	memoryvfs.RegisterMemoryVfs()

	utils.Logger.Info("Registering Local VFS.")
	if err := localvfs.RegisterLocalVfs(); err != nil {
		utils.Logger.Error("Failed to register Local VFS: " + err.Error())
		return err
	}

	utils.Logger.Info("Registering Remote VFS.")
	remotevfs.RegisterRemoteVfs()

	return nil
}
