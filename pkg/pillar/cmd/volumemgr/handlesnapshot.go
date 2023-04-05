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
	snapshotStatus := lookupVolumesSnapshotStatus(ctx, config.SnapshotID)
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
		VolumeSnapshotMeta:   make(map[uuid.UUID]interface{}, len(config.VolumeIDs)),
	}
	// Find the corresponding volume status
	for _, volumeID := range config.VolumeIDs {
		volumeStatus := ctx.lookupVolumeStatusByUUID(volumeID)
		if volumeStatus == nil {
			log.Errorf("handleVolumesSnapshotCreate: volume %s not found", volumeID.String())
			// TODO Set the error in the status, clean the snapshotStatus
		}
		snapshotMeta, err := createVolumeSnapshot(ctx, volumeStatus)
		if err != nil {
			log.Errorf("handleVolumesSnapshotCreate: failed to create volume snapshot for %s, %s", volumeID.String(), err.Error())
			// TODO Set the error in the status
		}
		snapshotStatus.VolumeSnapshotMeta[volumeID] = snapshotMeta

	}
	publishVolumesSnapshotStatus(ctx, snapshotStatus)
}

func createVolumeSnapshot(ctx *volumemgrContext, volumeStatus *types.VolumeStatus) (interface{}, error) {
	volumeHandlers := volumehandlers.GetVolumeHandler(log, ctx, volumeStatus)
	snapshotMeta, err := volumeHandlers.CreateSnapshot()
	if err != nil {
		log.Errorf("createVolumeSnapshot: failed to create snapshot for %s, %s", volumeStatus.VolumeID.String(), err.Error())
		return "", err
	}
	return snapshotMeta, nil
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
	snapshotStatus := lookupVolumesSnapshotStatus(ctx, config.SnapshotID)
	if snapshotStatus == nil {
		log.Errorf("handleVolumesSnapshotModify: snapshot %s not found", key)
		// TODO How to handle this case?
		return
	}
	for volumeID, snapMeta := range snapshotStatus.VolumeSnapshotMeta {
		volumeStatus := ctx.lookupVolumeStatusByUUID(volumeID)
		if volumeStatus == nil {
			log.Errorf("handleVolumesSnapshotModify: volume %s not found", volumeID.String())
			// TODO Set the error in the status
			return
		}
		switch config.Action {
		case types.VolumesSnapshotRollback:
			err := rollbackToSnapshot(ctx, volumeStatus, snapMeta)
			if err != nil {
				log.Errorf("Failed to rollback to snapshot with ID %s, %s", config.SnapshotID, err.Error())
				// TODO Set the error in the status
			}
		case types.VolumesSnapshotDelete:
			err := deleteSnapshot(ctx, volumeStatus, snapMeta)
			if err != nil {
				log.Errorf("Failed to delete snapshot with ID %s, %s", config.SnapshotID, err.Error())
				// TODO Set the error in the status
			}
		default:
			log.Errorf("handleVolumesSnapshotModify: unexpected action %s", config.Action)
			return
		}

	}
	// TODO: implement for rollback, delete
	publishVolumesSnapshotStatus(ctx, snapshotStatus)
	log.Functionf("handleVolumesSnapshotConfigImpl(%s) done", key)
}

func rollbackToSnapshot(ctx *volumemgrContext, status *types.VolumeStatus, meta interface{}) error {
	volumeHandlers := volumehandlers.GetVolumeHandler(log, ctx, status)
	err := volumeHandlers.RollbackToSnapshot(meta)
	if err != nil {
		log.Errorf("rollbackToSnapshot: failed to rollback to snapshot for %s, %s", status.VolumeID.String(), err.Error())
		// TODO Set the error in the status
		return err
	}
	return nil
}

func deleteSnapshot(ctx *volumemgrContext, status *types.VolumeStatus, meta interface{}) error {
	volumeHandlers := volumehandlers.GetVolumeHandler(log, ctx, status)
	err := volumeHandlers.DeleteSnapshot(meta)
	if err != nil {
		log.Errorf("deleteSnapshot: failed to delete snapshot for %s, %s", status.VolumeID.String(), err.Error())
		// TODO Set the error in the status
		return err
	}
	return nil
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