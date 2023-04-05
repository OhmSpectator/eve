// Copyright (c) 2013-2023 Zededa,
// SPDX-License-Identifier: Apache-2.0

package volumemgr

import (
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/volumehandlers"
	"github.com/satori/go.uuid"
)

func handleVolumesSnapshotCreate(ctxArg interface{}, key string, configArg interface{}) {
	ctx := ctxArg.(*volumemgrContext)
	config := configArg.(types.VolumesSnapshotConfig)
	log.Functionf("handleVolumesSnapshotCreate(%s) handles %s", key, config.Action)
	if config.Action != types.VolumesSnapshotCreate {
		log.Errorf("handleVolumesSnapshotCreate: unexpected action %s", config.Action)
		// TODO Set the error in the status
		return
	}
	// Check if snapshot snapshotStatus already exists, or it's a new snapshot request
	snapshotStatus := lookupVolumesSnapshotStatus(ctx, key)
	if snapshotStatus != nil {
		log.Errorf("handleVolumesSnapshotCreate: snapshot %s already exists", key)
		// TODO How to handle this case?
		return
	}
	// Create a new snapshotStatus
	snapshotStatus = &types.VolumesSnapshotStatus{
		SnapshotID: config.SnapshotID,
		// Save the config UUID and version, so it can be reported later to the controller during the rollback
		ConfigUUIDAndVersion: config.ConfigUUIDAndVersion,
		VolumeSnapshotFiles:  make(map[uuid.UUID]string, len(config.VolumeIDs)),
	}
	// Find the corresponding volume status and get the file location
	for _, volumeID := range config.VolumeIDs {
		volumeStatus := ctx.lookupVolumeStatusByUUID(volumeID)
		if volumeStatus == nil {
			log.Errorf("handleVolumesSnapshotCreate: volume %s not found", volumeID.String())
			// TODO Set the error in the status, clean the snapshotStatus
		}
		snapshotFile, err := createVolumeSnapshot(ctx, volumeStatus)
		if err != nil {
			log.Errorf("handleVolumesSnapshotCreate: failed to create volume snapshot for %s, %s", volumeID.String(), err.Error())
			// TODO Set the error in the status
		}
		snapshotStatus.VolumeSnapshotFiles[volumeID] = snapshotFile

	}
	publishVolumesSnapshotStatus(ctx, snapshotStatus)
}

func createVolumeSnapshot(ctx *volumemgrContext, volumeStatus *types.VolumeStatus) (string, error) {
	volumeHandlers := volumehandlers.GetVolumeHandler(log, ctx, volumeStatus)
	snapshotFile, err := volumeHandlers.CreateSnapshot()
	if err != nil {
		log.Errorf("createVolumeSnapshot: failed to create snapshot for %s, %s", volumeStatus.VolumeID.String(), err.Error())
		return "", err
	}
	log.Functionf("createVolumeSnapshot: snapshot file: %s", snapshotFile)
	return snapshotFile, nil
}

func handleVolumesSnapshotModify(ctxArg interface{}, key string, configArg, _ interface{}) {
	ctx := ctxArg.(*volumemgrContext)
	config := configArg.(types.VolumesSnapshotConfig)
	log.Functionf("handleVolumesSnapshotModify(%s) handles %s", key, config.Action)
	if config.Action != types.VolumesSnapshotRollback && config.Action != types.VolumesSnapshotDelete {
		log.Errorf("handleVolumesSnapshotModify: unexpected action %s", config.Action)
		// TODO Set the error in the status
		return
	}
	// Check if snapshot status already exists, or it's a new snapshot request
	snapshotStatus := lookupVolumesSnapshotStatus(ctx, key)
	if snapshotStatus == nil {
		log.Errorf("handleVolumesSnapshotModify: snapshot %s not found", key)
		// TODO How to handle this case?
		return
	}
	// TODO: implement for rollback, delete
	publishVolumesSnapshotStatus(ctx, snapshotStatus)
	log.Functionf("handleVolumesSnapshotConfigImpl(%s) done", key)
}

func publishVolumesSnapshotStatus(ctx *volumemgrContext, status *types.VolumesSnapshotStatus) {
	key := status.Key()
	log.Functionf("publishVolumesSnapshotStatus(%s)", key)
	pub := ctx.pubVolumesSnapshotStatus
	_ = pub.Publish(key, *status)
}

func lookupVolumesSnapshotStatus(ctx *volumemgrContext, key string) *types.VolumesSnapshotStatus {
	sub := ctx.pubVolumesSnapshotStatus
	st, _ := sub.Get(key)
	if st == nil {
		return nil
	}
	status := st.(types.VolumesSnapshotStatus)
	return &status
}
