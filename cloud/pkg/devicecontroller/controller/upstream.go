/*
Copyright 2019 The KubeEdge Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kubeedge/api/apis/devices/v1beta1"
	crdClientset "github.com/kubeedge/api/client/clientset/versioned"
	"github.com/kubeedge/api/client/clientset/versioned/scheme"
	beehiveContext "github.com/kubeedge/beehive/pkg/core/context"
	"github.com/kubeedge/beehive/pkg/core/model"
	keclient "github.com/kubeedge/kubeedge/cloud/pkg/common/client"
	utilcontext "github.com/kubeedge/kubeedge/cloud/pkg/common/context"
	"github.com/kubeedge/kubeedge/cloud/pkg/common/messagelayer"
	"github.com/kubeedge/kubeedge/cloud/pkg/common/modules"
	"github.com/kubeedge/kubeedge/cloud/pkg/devicecontroller/config"
	"github.com/kubeedge/kubeedge/cloud/pkg/devicecontroller/constants"
	"github.com/kubeedge/kubeedge/cloud/pkg/devicecontroller/types"
	commonconst "github.com/kubeedge/kubeedge/common/constants"
)

// DeviceStatus is structure to patch device status
type DeviceStatus struct {
	Status v1beta1.DeviceStatusStatus `json:"status"`
}

// DeviceStatusCRDPatch is structure to patch device status CRD with Extensions excluded
type DeviceStatusCRDPatch struct {
	Status DeviceStatusStatusPatch `json:"status"`
}

// DeviceStatusStatusPatch is structure to patch device status status with Extensions excluded
type DeviceStatusStatusPatch struct {
	Twins          []v1beta1.Twin `json:"twins,omitempty"`
	State          string         `json:"state,omitempty"`
	LastOnlineTime string         `json:"lastOnlineTime,omitempty"`
}

const (
	// MergePatchType is patch type
	MergePatchType = "application/merge-patch+json"
	// ResourceTypeDevices is plural of device resource in apiserver
	ResourceTypeDevices = "devices"
	// ResourceTypeDeviceStatuses is plural of device status resource in apiserver
	ResourceTypeDeviceStatuses = "devicestatuses"
)

// UpstreamController subscribe messages from edge and sync to k8s api server
type UpstreamController struct {
	crdClient    crdClientset.Interface
	messageLayer messagelayer.MessageLayer
	// deviceTwinsChan message channel
	deviceTwinsChan chan model.Message
	// deviceStates message channel
	deviceStatesChan chan model.Message
	// downstream controller to update device status in cache
	dc *DownstreamController
	// twinsCache caches twins for each device to avoid frequent API server queries
	// key: deviceID, value: *cachedDeviceStatus
	twinsCache sync.Map
}

// cachedDeviceStatus holds twins and ResourceVersion for optimistic locking
type cachedDeviceStatus struct {
	twins           []v1beta1.Twin
	resourceVersion string
}

// Start UpstreamController
func (uc *UpstreamController) Start() error {
	klog.Info("Start upstream devicecontroller")

	uc.deviceTwinsChan = make(chan model.Message, config.Config.Buffer.UpdateDeviceTwins)
	uc.deviceStatesChan = make(chan model.Message, config.Config.Buffer.UpdateDeviceStates)
	go uc.dispatchMessage()

	for i := 0; i < int(config.Config.Load.UpdateDeviceStatusWorkers); i++ {
		go uc.updateDeviceStatus()
	}
	return nil
}

func (uc *UpstreamController) dispatchMessage() {
	for {
		select {
		case <-beehiveContext.Done():
			klog.Info("Stop dispatchMessage")
			return
		default:
		}
		msg, err := uc.messageLayer.Receive()
		if err != nil {
			klog.Warningf("Receive message failed, %s", err)
			continue
		}

		klog.Infof("Dispatch message: %s", msg.GetID())

		resourceType, err := messagelayer.GetResourceTypeForDevice(msg.GetResource())
		if err != nil {
			klog.Warningf("Parse message: %s resource type with error: %s", msg.GetID(), err)
			continue
		}
		klog.Infof("Message: %s, resource type is: %s", msg.GetID(), resourceType)

		switch resourceType {
		case constants.ResourceTypeTwinEdgeUpdated:
			uc.deviceTwinsChan <- msg
		case constants.ResourceDeviceStateUpdated:
			uc.deviceStatesChan <- msg
		case constants.ResourceTypeMembershipDetail:
		default:
			klog.Warningf("Message: %s, with resource type: %s not intended for device controller", msg.GetID(), resourceType)
		}
	}
}

func (uc *UpstreamController) updateDeviceStatus() {
	for {
		select {
		case <-beehiveContext.Done():
			klog.Info("Stop updateDeviceStatus")
			return
		case msg := <-uc.deviceStatesChan:
			klog.Infof("Message: %s, operation is: %s, and resource is: %s", msg.GetID(), msg.GetOperation(), msg.GetResource())
			msgState, err := uc.unmarshalDeviceStatesMessage(msg)
			if err != nil {
				klog.Warningf("Unmarshall failed due to error %v", err)
				continue
			}
			deviceID, err := messagelayer.GetDeviceID(msg.GetResource())
			if err != nil {
				klog.Warning("Failed to get device id")
				continue
			}

			device, ok := uc.dc.deviceManager.Device.Load(deviceID)
			if !ok {
				klog.Warningf("Device %s does not exist in upstream controller", deviceID)
				continue
			}
			cacheDevice, ok := device.(*v1beta1.Device)
			if !ok {
				klog.Warning("Failed to assert to CacheDevice type")
				continue
			}

			deviceStatusApply := &v1beta1.DeviceStatus{
				TypeMeta: metav1.TypeMeta{
					APIVersion: v1beta1.SchemeGroupVersion.String(),
					Kind:       "DeviceStatus",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      cacheDevice.Name,
					Namespace: cacheDevice.Namespace,
				},
				Status: v1beta1.DeviceStatusStatus{
					State:          msgState.Device.State,
					LastOnlineTime: msgState.Device.LastOnlineTime,
				},
			}

			if err := controllerutil.SetControllerReference(cacheDevice, deviceStatusApply, scheme.Scheme); err != nil {
				klog.Errorf("Failed to set controller reference for device %s: %v", deviceID, err)
				continue
			}

			body, err := json.Marshal(deviceStatusApply)
			if err != nil {
				klog.Errorf("Failed to marshal device states %v", deviceStatusApply.Status)
				continue
			}
			forceApply := true
			_, err = uc.crdClient.DevicesV1beta1().DeviceStatuses(deviceStatusApply.Namespace).Patch(
				context.Background(),
				deviceStatusApply.Name,
				k8stypes.ApplyPatchType,
				body,
				metav1.PatchOptions{FieldManager: modules.DeviceControllerModuleName, Force: &forceApply},
			)
			if err != nil {
				klog.Errorf("Failed to apply device states %v of device %v in namespace %v, err: %v", deviceStatusApply,
					deviceID, deviceStatusApply.Namespace, err)
				continue
			}

			//send confirm message to edge twin
			resMsg := model.NewMessage(msg.GetID())
			nodeID, err := messagelayer.GetNodeID(msg)
			if err != nil {
				klog.Warningf("Message: %s process failure, get node id failed with error: %s", msg.GetID(), err)
				continue
			}
			resource, err := messagelayer.BuildResourceForDevice(nodeID, "twin", "")
			if err != nil {
				klog.Warningf("Message: %s process failure, build message resource failed with error: %s", msg.GetID(), err)
				continue
			}
			resMsg.BuildRouter(modules.DeviceControllerModuleName, constants.GroupTwin, resource, model.ResponseOperation)
			resMsg.Content = commonconst.MessageSuccessfulContent
			err = uc.messageLayer.Response(*resMsg)
			if err != nil {
				klog.Warningf("Message: %s process failure, response failed with error: %s", msg.GetID(), err)
				continue
			}
			klog.Infof("Message: %s process successfully", msg.GetID())
		case msg := <-uc.deviceTwinsChan:
			klog.Infof("Message: %s, operation is: %s, and resource is: %s", msg.GetID(), msg.GetOperation(), msg.GetResource())
			msgTwin, err := uc.unmarshalDeviceStatusMessage(msg)
			if err != nil {
				klog.Warningf("Unmarshall failed due to error %v", err)
				continue
			}
			deviceID, err := messagelayer.GetDeviceID(msg.GetResource())
			if err != nil {
				klog.Warning("Failed to get device id")
				continue
			}

			device, ok := uc.dc.deviceManager.Device.Load(deviceID)
			if !ok {
				klog.Warningf("Device %s does not exist in downstream controller", deviceID)
				continue
			}
			cacheDevice, ok := device.(*v1beta1.Device)
			if !ok {
				klog.Warning("Failed to assert to CacheDevice type")
				continue
			}

			// Retry loop for optimistic locking
			const maxRetries = 3
			for retry := 0; retry < maxRetries; retry++ {
				if retry > 0 {
					klog.Infof("Retrying twin update for device %s, attempt %d/%d", deviceID, retry+1, maxRetries)
				}

				// Try to get twins and ResourceVersion from cache first
				var twins []v1beta1.Twin
				var resourceVersion string
				if cached, found := uc.twinsCache.Load(deviceID); found {
					cachedStatus := cached.(*cachedDeviceStatus)
					twins = cachedStatus.twins
					resourceVersion = cachedStatus.resourceVersion
				} else {
					// Cache miss, fetch from API server
					existingDeviceStatus, err := uc.crdClient.DevicesV1beta1().DeviceStatuses(cacheDevice.Namespace).Get(
						context.Background(),
						cacheDevice.Name,
						metav1.GetOptions{},
					)
					if err == nil && existingDeviceStatus != nil {
						twins = existingDeviceStatus.Status.Twins
						resourceVersion = existingDeviceStatus.ResourceVersion
					}
				}

				// Update or append twins from message
				for twinName, twin := range msgTwin.Twin {
					// Only process twins that are defined in device properties
					if !isPropertyDefined(twinName, cacheDevice.Spec.Properties) {
						continue
					}

					// Find existing twin or create new one
					twinIndex := -1
					for i := range twins {
						if twins[i].PropertyName == twinName {
							twinIndex = i
							break
						}
					}

					deviceTwin := v1beta1.Twin{
						PropertyName: twinName,
					}

					if twin.Actual != nil && twin.Actual.Value != nil {
						reported := v1beta1.TwinProperty{}
						reported.Value = *twin.Actual.Value
						reported.Metadata = make(map[string]string)
						if twin.Actual.Metadata != nil {
							reported.Metadata["timestamp"] = strconv.FormatInt(twin.Actual.Metadata.Timestamp, 10)
						}
						if twin.Metadata != nil {
							reported.Metadata["type"] = twin.Metadata.Type
						}
						deviceTwin.Reported = reported
					}

					if twin.Expected != nil && twin.Expected.Value != nil {
						observedDesired := v1beta1.TwinProperty{}
						observedDesired.Value = *twin.Expected.Value
						observedDesired.Metadata = make(map[string]string)
						if twin.Expected.Metadata != nil {
							observedDesired.Metadata["timestamp"] = strconv.FormatInt(twin.Expected.Metadata.Timestamp, 10)
						}
						if twin.Metadata != nil {
							observedDesired.Metadata["type"] = twin.Metadata.Type
						}
						deviceTwin.ObservedDesired = observedDesired
					}

					// Update existing twin or append new one
					if twinIndex >= 0 {
						twins[twinIndex] = deviceTwin
					} else {
						twins = append(twins, deviceTwin)
					}
				}

				deviceStatusApply := &v1beta1.DeviceStatus{
					TypeMeta: metav1.TypeMeta{
						APIVersion: v1beta1.SchemeGroupVersion.String(),
						Kind:       "DeviceStatus",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:            cacheDevice.Name,
						Namespace:       cacheDevice.Namespace,
						ResourceVersion: resourceVersion,
					},
					Status: v1beta1.DeviceStatusStatus{
						Twins: twins,
						Extensions: v1beta1.DeviceStatusExtensions{
							Data: map[string]interface{}{},
						},
					},
				}

				if err := controllerutil.SetControllerReference(cacheDevice, deviceStatusApply, scheme.Scheme); err != nil {
					klog.Errorf("Failed to set controller reference for device %s: %v", deviceID, err)
					continue
				}

				body, err := json.Marshal(deviceStatusApply)
				if err != nil {
					klog.Errorf("Failed to marshal device status %v", deviceStatusApply.Status)
					break
				}

				// Use SSA without Force to detect conflicts
				patchedStatus, err := uc.crdClient.DevicesV1beta1().DeviceStatuses(cacheDevice.Namespace).Patch(
					utilcontext.FromMessage(context.Background(), msg),
					cacheDevice.Name,
					k8stypes.ApplyPatchType,
					body,
					metav1.PatchOptions{FieldManager: modules.DeviceControllerModuleName},
				)
				if err != nil {
					// Check if it's a conflict error
					if retry < maxRetries-1 {
						// Invalidate cache and retry
						uc.twinsCache.Delete(deviceID)
						klog.Warningf("Conflict detected for device %s, will retry: %v", deviceID, err)
						continue
					}
					klog.Errorf("Failed to apply device status %v of device %v in namespace %v after %d retries, err: %v", deviceStatusApply.Status, deviceID, cacheDevice.Namespace, maxRetries, err)
					break
				}

				// Update cache with new ResourceVersion after successful patch
				uc.twinsCache.Store(deviceID, &cachedDeviceStatus{
					twins:           twins,
					resourceVersion: patchedStatus.ResourceVersion,
				})

				// Success, break retry loop
				break
			}

			//send confirm message to edge twin
			resMsg := model.NewMessage(msg.GetID())
			nodeID, err := messagelayer.GetNodeID(msg)
			if err != nil {
				klog.Warningf("Message: %s process failure, get node id failed with error: %s", msg.GetID(), err)
				continue
			}
			resource, err := messagelayer.BuildResourceForDevice(nodeID, "twin", "")
			if err != nil {
				klog.Warningf("Message: %s process failure, build message resource failed with error: %s", msg.GetID(), err)
				continue
			}
			resMsg.BuildRouter(modules.DeviceControllerModuleName, constants.GroupTwin, resource, model.ResponseOperation)
			resMsg.Content = commonconst.MessageSuccessfulContent
			err = uc.messageLayer.Response(*resMsg)
			if err != nil {
				klog.Warningf("Message: %s process failure, response failed with error: %s", msg.GetID(), err)
				continue
			}
			klog.Infof("Message: %s process successfully", msg.GetID())
		}
	}
}

func (uc *UpstreamController) unmarshalDeviceStatusMessage(msg model.Message) (*types.DeviceTwinUpdate, error) {
	contentData, err := msg.GetContentData()
	if err != nil {
		return nil, err
	}

	twinUpdate := &types.DeviceTwinUpdate{}
	if err := json.Unmarshal(contentData, twinUpdate); err != nil {
		return nil, err
	}
	return twinUpdate, nil
}

func (uc *UpstreamController) unmarshalDeviceStatesMessage(msg model.Message) (*types.DeviceStateUpdate, error) {
	contentData, err := msg.GetContentData()
	if err != nil {
		return nil, err
	}

	stateUpdate := &types.DeviceStateUpdate{}
	if err := json.Unmarshal(contentData, stateUpdate); err != nil {
		return nil, err
	}
	return stateUpdate, nil
}

// NewUpstreamController create UpstreamController from config
func NewUpstreamController(dc *DownstreamController) (*UpstreamController, error) {
	uc := &UpstreamController{
		crdClient:    keclient.GetCRDClient(),
		messageLayer: messagelayer.DeviceControllerMessageLayer(),
		dc:           dc,
	}
	return uc, nil
}

func isPropertyDefined(propertyName string, properties []v1beta1.DeviceProperty) bool {
	for i := range properties {
		if propertyName == properties[i].Name {
			return true
		}
	}
	return false
}

func findOrCreateTwinByName(twinName string, properties []v1beta1.DeviceProperty, deviceStatus *DeviceStatus) *v1beta1.Twin {
	for i := range properties {
		if twinName == properties[i].Name {
			twin := findTwinByName(twinName, deviceStatus)
			if twin != nil {
				return twin
			}
			twin = &v1beta1.Twin{
				PropertyName: twinName,
			}
			deviceStatus.Status.Twins = append(deviceStatus.Status.Twins, *twin)
			return twin
		}
	}
	return nil
}

func findTwinByName(twinName string, deviceStatus *DeviceStatus) *v1beta1.Twin {
	for i := range deviceStatus.Status.Twins {
		if twinName == deviceStatus.Status.Twins[i].PropertyName {
			return &deviceStatus.Status.Twins[i]
		}
	}
	return nil
}
