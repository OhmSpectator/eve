// Copyright(c) 2017-2018 Zededa, Inc.
// All rights reserved.

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.26.0
// 	protoc        v3.18.1
// source: config/baseosconfig.proto

package config

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// OS version key and value pair
type OSKeyTags struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *OSKeyTags) Reset() {
	*x = OSKeyTags{}
	if protoimpl.UnsafeEnabled {
		mi := &file_config_baseosconfig_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *OSKeyTags) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OSKeyTags) ProtoMessage() {}

func (x *OSKeyTags) ProtoReflect() protoreflect.Message {
	mi := &file_config_baseosconfig_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OSKeyTags.ProtoReflect.Descriptor instead.
func (*OSKeyTags) Descriptor() ([]byte, []int) {
	return file_config_baseosconfig_proto_rawDescGZIP(), []int{0}
}

// repeated key value tags compromising
type OSVerDetails struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *OSVerDetails) Reset() {
	*x = OSVerDetails{}
	if protoimpl.UnsafeEnabled {
		mi := &file_config_baseosconfig_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *OSVerDetails) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OSVerDetails) ProtoMessage() {}

func (x *OSVerDetails) ProtoReflect() protoreflect.Message {
	mi := &file_config_baseosconfig_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OSVerDetails.ProtoReflect.Descriptor instead.
func (*OSVerDetails) Descriptor() ([]byte, []int) {
	return file_config_baseosconfig_proto_rawDescGZIP(), []int{1}
}

type BaseOSConfig struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Uuidandversion *UUIDandVersion `protobuf:"bytes,1,opt,name=uuidandversion,proto3" json:"uuidandversion,omitempty"`
	// volumeID will supersede drives. Drives still
	// exist for backward compatibility.
	// Drives will be deprecated in the future.
	Drives        []*Drive `protobuf:"bytes,3,rep,name=drives,proto3" json:"drives,omitempty"`
	Activate      bool     `protobuf:"varint,4,opt,name=activate,proto3" json:"activate,omitempty"`
	BaseOSVersion string   `protobuf:"bytes,10,opt,name=baseOSVersion,proto3" json:"baseOSVersion,omitempty"` // deprecated 11; OSVerDetails baseOSDetails
	VolumeID      string   `protobuf:"bytes,12,opt,name=volumeID,proto3" json:"volumeID,omitempty"`           // UUID for Volume with BaseOS image
}

func (x *BaseOSConfig) Reset() {
	*x = BaseOSConfig{}
	if protoimpl.UnsafeEnabled {
		mi := &file_config_baseosconfig_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *BaseOSConfig) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*BaseOSConfig) ProtoMessage() {}

func (x *BaseOSConfig) ProtoReflect() protoreflect.Message {
	mi := &file_config_baseosconfig_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use BaseOSConfig.ProtoReflect.Descriptor instead.
func (*BaseOSConfig) Descriptor() ([]byte, []int) {
	return file_config_baseosconfig_proto_rawDescGZIP(), []int{2}
}

func (x *BaseOSConfig) GetUuidandversion() *UUIDandVersion {
	if x != nil {
		return x.Uuidandversion
	}
	return nil
}

func (x *BaseOSConfig) GetDrives() []*Drive {
	if x != nil {
		return x.Drives
	}
	return nil
}

func (x *BaseOSConfig) GetActivate() bool {
	if x != nil {
		return x.Activate
	}
	return false
}

func (x *BaseOSConfig) GetBaseOSVersion() string {
	if x != nil {
		return x.BaseOSVersion
	}
	return ""
}

func (x *BaseOSConfig) GetVolumeID() string {
	if x != nil {
		return x.VolumeID
	}
	return ""
}

type BaseOS struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// UUID for ContentTree with BaseOS image
	// DisplayName of ContentTree should contain
	// BaseOS version
	ContentTreeUuid string `protobuf:"bytes,1,opt,name=content_tree_uuid,json=contentTreeUuid,proto3" json:"content_tree_uuid,omitempty"`
	// retry_update
	// Retry the BaseOs update if the update failed previously.
	// 1) If this image is in FAILED state, retry the image update.
	// 2) If this image is already active and fully installed (PartitionState = UPDATED),
	//    Do nothing. Just update the baseos_update_counter in Info message.
	// 3) If this image is same as active image, but status is NOT yet UPDATED, or
	//    if the update to this image is in progress, wait till the update
	//    concludes (Success / Error+rollback) - then trigger the retry as needed.
	RetryUpdate *DeviceOpsCmd `protobuf:"bytes,2,opt,name=retry_update,json=retryUpdate,proto3" json:"retry_update,omitempty"`
	// if not set BaseOS will be installed,
	// but not activated
	Activate bool `protobuf:"varint,3,opt,name=activate,proto3" json:"activate,omitempty"`
}

func (x *BaseOS) Reset() {
	*x = BaseOS{}
	if protoimpl.UnsafeEnabled {
		mi := &file_config_baseosconfig_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *BaseOS) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*BaseOS) ProtoMessage() {}

func (x *BaseOS) ProtoReflect() protoreflect.Message {
	mi := &file_config_baseosconfig_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use BaseOS.ProtoReflect.Descriptor instead.
func (*BaseOS) Descriptor() ([]byte, []int) {
	return file_config_baseosconfig_proto_rawDescGZIP(), []int{3}
}

func (x *BaseOS) GetContentTreeUuid() string {
	if x != nil {
		return x.ContentTreeUuid
	}
	return ""
}

func (x *BaseOS) GetRetryUpdate() *DeviceOpsCmd {
	if x != nil {
		return x.RetryUpdate
	}
	return nil
}

func (x *BaseOS) GetActivate() bool {
	if x != nil {
		return x.Activate
	}
	return false
}

var File_config_baseosconfig_proto protoreflect.FileDescriptor

var file_config_baseosconfig_proto_rawDesc = []byte{
	0x0a, 0x19, 0x63, 0x6f, 0x6e, 0x66, 0x69, 0x67, 0x2f, 0x62, 0x61, 0x73, 0x65, 0x6f, 0x73, 0x63,
	0x6f, 0x6e, 0x66, 0x69, 0x67, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x15, 0x6f, 0x72, 0x67,
	0x2e, 0x6c, 0x66, 0x65, 0x64, 0x67, 0x65, 0x2e, 0x65, 0x76, 0x65, 0x2e, 0x63, 0x6f, 0x6e, 0x66,
	0x69, 0x67, 0x1a, 0x16, 0x63, 0x6f, 0x6e, 0x66, 0x69, 0x67, 0x2f, 0x64, 0x65, 0x76, 0x63, 0x6f,
	0x6d, 0x6d, 0x6f, 0x6e, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x14, 0x63, 0x6f, 0x6e, 0x66,
	0x69, 0x67, 0x2f, 0x73, 0x74, 0x6f, 0x72, 0x61, 0x67, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x22, 0x0b, 0x0a, 0x09, 0x4f, 0x53, 0x4b, 0x65, 0x79, 0x54, 0x61, 0x67, 0x73, 0x22, 0x0e, 0x0a,
	0x0c, 0x4f, 0x53, 0x56, 0x65, 0x72, 0x44, 0x65, 0x74, 0x61, 0x69, 0x6c, 0x73, 0x22, 0xf1, 0x01,
	0x0a, 0x0c, 0x42, 0x61, 0x73, 0x65, 0x4f, 0x53, 0x43, 0x6f, 0x6e, 0x66, 0x69, 0x67, 0x12, 0x4d,
	0x0a, 0x0e, 0x75, 0x75, 0x69, 0x64, 0x61, 0x6e, 0x64, 0x76, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e,
	0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x25, 0x2e, 0x6f, 0x72, 0x67, 0x2e, 0x6c, 0x66, 0x65,
	0x64, 0x67, 0x65, 0x2e, 0x65, 0x76, 0x65, 0x2e, 0x63, 0x6f, 0x6e, 0x66, 0x69, 0x67, 0x2e, 0x55,
	0x55, 0x49, 0x44, 0x61, 0x6e, 0x64, 0x56, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x52, 0x0e, 0x75,
	0x75, 0x69, 0x64, 0x61, 0x6e, 0x64, 0x76, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x12, 0x34, 0x0a,
	0x06, 0x64, 0x72, 0x69, 0x76, 0x65, 0x73, 0x18, 0x03, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x1c, 0x2e,
	0x6f, 0x72, 0x67, 0x2e, 0x6c, 0x66, 0x65, 0x64, 0x67, 0x65, 0x2e, 0x65, 0x76, 0x65, 0x2e, 0x63,
	0x6f, 0x6e, 0x66, 0x69, 0x67, 0x2e, 0x44, 0x72, 0x69, 0x76, 0x65, 0x52, 0x06, 0x64, 0x72, 0x69,
	0x76, 0x65, 0x73, 0x12, 0x1a, 0x0a, 0x08, 0x61, 0x63, 0x74, 0x69, 0x76, 0x61, 0x74, 0x65, 0x18,
	0x04, 0x20, 0x01, 0x28, 0x08, 0x52, 0x08, 0x61, 0x63, 0x74, 0x69, 0x76, 0x61, 0x74, 0x65, 0x12,
	0x24, 0x0a, 0x0d, 0x62, 0x61, 0x73, 0x65, 0x4f, 0x53, 0x56, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e,
	0x18, 0x0a, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0d, 0x62, 0x61, 0x73, 0x65, 0x4f, 0x53, 0x56, 0x65,
	0x72, 0x73, 0x69, 0x6f, 0x6e, 0x12, 0x1a, 0x0a, 0x08, 0x76, 0x6f, 0x6c, 0x75, 0x6d, 0x65, 0x49,
	0x44, 0x18, 0x0c, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x76, 0x6f, 0x6c, 0x75, 0x6d, 0x65, 0x49,
	0x44, 0x22, 0x98, 0x01, 0x0a, 0x06, 0x42, 0x61, 0x73, 0x65, 0x4f, 0x53, 0x12, 0x2a, 0x0a, 0x11,
	0x63, 0x6f, 0x6e, 0x74, 0x65, 0x6e, 0x74, 0x5f, 0x74, 0x72, 0x65, 0x65, 0x5f, 0x75, 0x75, 0x69,
	0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0f, 0x63, 0x6f, 0x6e, 0x74, 0x65, 0x6e, 0x74,
	0x54, 0x72, 0x65, 0x65, 0x55, 0x75, 0x69, 0x64, 0x12, 0x46, 0x0a, 0x0c, 0x72, 0x65, 0x74, 0x72,
	0x79, 0x5f, 0x75, 0x70, 0x64, 0x61, 0x74, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x23,
	0x2e, 0x6f, 0x72, 0x67, 0x2e, 0x6c, 0x66, 0x65, 0x64, 0x67, 0x65, 0x2e, 0x65, 0x76, 0x65, 0x2e,
	0x63, 0x6f, 0x6e, 0x66, 0x69, 0x67, 0x2e, 0x44, 0x65, 0x76, 0x69, 0x63, 0x65, 0x4f, 0x70, 0x73,
	0x43, 0x6d, 0x64, 0x52, 0x0b, 0x72, 0x65, 0x74, 0x72, 0x79, 0x55, 0x70, 0x64, 0x61, 0x74, 0x65,
	0x12, 0x1a, 0x0a, 0x08, 0x61, 0x63, 0x74, 0x69, 0x76, 0x61, 0x74, 0x65, 0x18, 0x03, 0x20, 0x01,
	0x28, 0x08, 0x52, 0x08, 0x61, 0x63, 0x74, 0x69, 0x76, 0x61, 0x74, 0x65, 0x42, 0x3d, 0x0a, 0x15,
	0x6f, 0x72, 0x67, 0x2e, 0x6c, 0x66, 0x65, 0x64, 0x67, 0x65, 0x2e, 0x65, 0x76, 0x65, 0x2e, 0x63,
	0x6f, 0x6e, 0x66, 0x69, 0x67, 0x5a, 0x24, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f,
	0x6d, 0x2f, 0x6c, 0x66, 0x2d, 0x65, 0x64, 0x67, 0x65, 0x2f, 0x65, 0x76, 0x65, 0x2f, 0x61, 0x70,
	0x69, 0x2f, 0x67, 0x6f, 0x2f, 0x63, 0x6f, 0x6e, 0x66, 0x69, 0x67, 0x62, 0x06, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x33,
}

var (
	file_config_baseosconfig_proto_rawDescOnce sync.Once
	file_config_baseosconfig_proto_rawDescData = file_config_baseosconfig_proto_rawDesc
)

func file_config_baseosconfig_proto_rawDescGZIP() []byte {
	file_config_baseosconfig_proto_rawDescOnce.Do(func() {
		file_config_baseosconfig_proto_rawDescData = protoimpl.X.CompressGZIP(file_config_baseosconfig_proto_rawDescData)
	})
	return file_config_baseosconfig_proto_rawDescData
}

var file_config_baseosconfig_proto_msgTypes = make([]protoimpl.MessageInfo, 4)
var file_config_baseosconfig_proto_goTypes = []interface{}{
	(*OSKeyTags)(nil),      // 0: org.lfedge.eve.config.OSKeyTags
	(*OSVerDetails)(nil),   // 1: org.lfedge.eve.config.OSVerDetails
	(*BaseOSConfig)(nil),   // 2: org.lfedge.eve.config.BaseOSConfig
	(*BaseOS)(nil),         // 3: org.lfedge.eve.config.BaseOS
	(*UUIDandVersion)(nil), // 4: org.lfedge.eve.config.UUIDandVersion
	(*Drive)(nil),          // 5: org.lfedge.eve.config.Drive
	(*DeviceOpsCmd)(nil),   // 6: org.lfedge.eve.config.DeviceOpsCmd
}
var file_config_baseosconfig_proto_depIdxs = []int32{
	4, // 0: org.lfedge.eve.config.BaseOSConfig.uuidandversion:type_name -> org.lfedge.eve.config.UUIDandVersion
	5, // 1: org.lfedge.eve.config.BaseOSConfig.drives:type_name -> org.lfedge.eve.config.Drive
	6, // 2: org.lfedge.eve.config.BaseOS.retry_update:type_name -> org.lfedge.eve.config.DeviceOpsCmd
	3, // [3:3] is the sub-list for method output_type
	3, // [3:3] is the sub-list for method input_type
	3, // [3:3] is the sub-list for extension type_name
	3, // [3:3] is the sub-list for extension extendee
	0, // [0:3] is the sub-list for field type_name
}

func init() { file_config_baseosconfig_proto_init() }
func file_config_baseosconfig_proto_init() {
	if File_config_baseosconfig_proto != nil {
		return
	}
	file_config_devcommon_proto_init()
	file_config_storage_proto_init()
	if !protoimpl.UnsafeEnabled {
		file_config_baseosconfig_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*OSKeyTags); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_config_baseosconfig_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*OSVerDetails); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_config_baseosconfig_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*BaseOSConfig); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_config_baseosconfig_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*BaseOS); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_config_baseosconfig_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   4,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_config_baseosconfig_proto_goTypes,
		DependencyIndexes: file_config_baseosconfig_proto_depIdxs,
		MessageInfos:      file_config_baseosconfig_proto_msgTypes,
	}.Build()
	File_config_baseosconfig_proto = out.File
	file_config_baseosconfig_proto_rawDesc = nil
	file_config_baseosconfig_proto_goTypes = nil
	file_config_baseosconfig_proto_depIdxs = nil
}
