// Copyright (c) 2013-2023 Zededa,
// SPDX-License-Identifier: Apache-2.0

package volumemgr

import (
	"fmt"
	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/volumehandlers"
	"time"
)

func handleVolumesSnapshotCreate(ctxArg interface{}, key string, configArg interface{}) {
	ctx := ctxArg.(*volumemgrContext)
	config := configArg.(types.VolumesSnapshotConfig)
	log.Functionf("handleVolumesSnapshotCreate(%s) handles %s", key, config.Action)
	log.Errorf("@ohm: : %s handles %s", key, config.Action)
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
		SnapshotID:         config.SnapshotID,
		VolumeSnapshotMeta: make(map[string]interface{}, len(config.VolumeIDs)),
		VolumeFormat:       make(map[string]zconfig.Format, len(config.VolumeIDs)),
		AppUUID:            config.AppUUID,
		// Save the config UUID and version, so it can be reported later to the controller during the rollback
	}
	// Find the corresponding volume status
	for _, volumeID := range config.VolumeIDs {
		volumeStatus := ctx.lookupVolumeStatusByUUID(volumeID.String())
		if volumeStatus == nil {
			errText := fmt.Sprintf("handleVolumesSnapshotCreate: volume %s not found", volumeID.String())
			log.Errorf(errText)
			errDescription := types.ErrorDescription{Error: errText}
			snapshotStatus.SetErrorWithSourceAndDescription(errDescription, types.VolumesSnapshotStatus{})
			publishVolumesSnapshotStatus(ctx, snapshotStatus)
			return
		}
		log.Errorf("@ohm: handleVolumesSnapshotCreate: volume %s found %s", volumeID.String(), volumeStatus.FileLocation)
		snapshotMeta, timeCreated, err := createVolumeSnapshot(ctx, volumeStatus)
		if err != nil {
			log.Errorf("handleVolumesSnapshotCreate: failed to create volume snapshot for %s, %s", volumeID.String(), err.Error())
			// TODO Set the error in the status
		}
		snapshotStatus.VolumeSnapshotMeta[volumeID.String()] = snapshotMeta
		snapshotStatus.VolumeFormat[volumeID.String()] = volumeStatus.ContentFormat

		//log.Noticef("@ohm: the value of volumehandler (id %s) is %v", volumeID.String(), snapshotStatus.VolumeHandler[volumeID.String()])
		//log.Noticef("@ohm: the new type of volumehandler (id %s) is %T", volumeID.String(), snapshotStatus.VolumeHandler[volumeID.String()])
		snapshotStatus.TimeCreated = timeCreated
	}
	log.Noticef("handleVolumesSnapshotCreate: snapshot status %v", snapshotStatus)
	log.Noticef("handleVolumesSnapshotCreate: successfully created snapshot %s", config.SnapshotID)
	publishVolumesSnapshotStatus(ctx, snapshotStatus)
}

func createVolumeSnapshot(ctx *volumemgrContext, volumeStatus *types.VolumeStatus) (interface{}, time.Time, error) {
	volumeHandlers := volumehandlers.GetVolumeHandler(log, ctx, volumeStatus)
	snapshotMeta, timeCreated, err := volumeHandlers.CreateSnapshot()
	log.Noticef("snapshotMeta: %s, timeCreated: %v, err: %v", snapshotMeta.(string), timeCreated, err)
	if err != nil {
		log.Errorf("createVolumeSnapshot: failed to create snapshot for %s, %s", volumeStatus.VolumeID.String(), err.Error())
		return "", timeCreated, err
	}
	return snapshotMeta, timeCreated, nil
}

func handleVolumesSnapshotModify(ctxArg interface{}, key string, configArg, _ interface{}) {
	ctx := ctxArg.(*volumemgrContext)
	config := configArg.(types.VolumesSnapshotConfig)
	log.Functionf("handleVolumesSnapshotModify(%s) handles %s", key, config.Action)
	log.Errorf("@ohm: handleVolumesSnapshotModify: %s handles %s", key, config.Action)
	if config.Action != types.VolumesSnapshotRollback {
		log.Errorf("handleVolumesSnapshotModify: unexpected action %s", config.Action)
		// TODO Set the error in the status
		return
	}
	// Check if snapshot status already exists, or it's a new snapshot request
	volumesSnapshotStatus := lookupVolumesSnapshotStatus(ctx, config.SnapshotID)
	if volumesSnapshotStatus == nil {
		log.Errorf("handleVolumesSnapshotModify: snapshot %s not found", key)
		// TODO How to handle this case?
		return
	}
	log.Noticef("handleVolumesSnapshotModify: snapshot status %v", volumesSnapshotStatus)
	for volumeID, snapMeta := range volumesSnapshotStatus.VolumeSnapshotMeta {
		volumeHandlers := volumehandlers.GetVolumeHandler(log, ctx, &types.VolumeStatus{
			ContentFormat: volumesSnapshotStatus.VolumeFormat[volumeID]})
		err := rollbackToSnapshot(volumeHandlers, snapMeta)
		if err != nil {
			log.Errorf("Failed to rollback to snapshot with ID %s, %s", config.SnapshotID, err.Error())
			return
			// TODO Set the error in the status
		}
	}
	publishVolumesSnapshotStatus(ctx, volumesSnapshotStatus)
	log.Noticef("handleVolumesSnapshotModify: successfully rolled back to snapshot %s", config.SnapshotID)
}

func handleVolumesSnapshotDelete(ctxArg interface{}, keyArg string, configArg interface{}) {
	ctx := ctxArg.(*volumemgrContext)
	config := configArg.(types.VolumesSnapshotConfig)
	log.Noticef("handleVolumesSnapshotDelete(%s)", keyArg)
	volumesSnapshotStatus := lookupVolumesSnapshotStatus(ctx, config.SnapshotID)
	if volumesSnapshotStatus == nil {
		log.Errorf("handleVolumesSnapshotDelete: snapshot status %s not found", keyArg)
		// TODO How to handle this case?
		return
	}

	log.Noticef("@ohm: volumesSnapshotStatus: %v", volumesSnapshotStatus)

	for volumeUUID, snapMeta := range volumesSnapshotStatus.VolumeSnapshotMeta {
		// Why is it not a VolumeHandler?
		volumeHandlers := volumehandlers.GetVolumeHandler(log, ctx, &types.VolumeStatus{
			ContentFormat: volumesSnapshotStatus.VolumeFormat[volumeUUID],
		})
		err := deleteSnapshot(volumeHandlers, snapMeta)
		if err != nil {
			log.Errorf("Failed to delete snapshot with ID %s, %s", config.SnapshotID, err.Error())
			// TODO Set the error in the status
			return
		}
	}
	unpublishVolumesSnapshotStatus(ctx, volumesSnapshotStatus)
	log.Noticef("handleVolumesSnapshotDelete(%s) done", keyArg)
}

func rollbackToSnapshot(volumeHandlers volumehandlers.VolumeHandler, meta interface{}) error {
	err := volumeHandlers.RollbackToSnapshot(meta)
	if err != nil {
		log.Errorf("rollbackToSnapshot: failed to rollback to snapshot")
		// TODO Set the error in the status
		return err
	}
	return nil
}

func deleteSnapshot(volumeHandlers volumehandlers.VolumeHandler, meta interface{}) error {
	err := volumeHandlers.DeleteSnapshot(meta)
	if err != nil {
		log.Errorf("deleteSnapshot: failed to delete snapshot")
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

func unpublishVolumesSnapshotStatus(ctx *volumemgrContext, status *types.VolumesSnapshotStatus) {
	key := status.Key()
	log.Functionf("unpublishVolumesSnapshotStatus(%s)", key)
	pub := ctx.pubVolumesSnapshotStatus
	pub.Unpublish(key)
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
