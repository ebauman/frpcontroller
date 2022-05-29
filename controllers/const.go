package controllers

import frpv1 "github.com/ebauman/frpcontroller/api/v1"

var (
	apiGVStr = frpv1.GroupVersion.String()
)

const (
	KindEndpoint = "Endpoint"
	KindService  = "Service"

	frpsFileName = "frps.ini"
	frpcFileName = "frpc.ini"

	annotationKeyEndpointPodConfigVersion = "frp.1eb100.net/config-version"
	annotationKeyServiceClusterIP         = "frp.1eb100.net/cluster-ip"
	labelKeyEndpointName                  = "frp.1eb100.net/endpoint"

	frpDockerImage = "vimagick/frp@sha256:215dee12e6cb41ccfb65be9a3a796e8e27ed9159cc5d5a54f536c28d07879e34"
)
