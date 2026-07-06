package tencent

import (
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	cvm2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke2022 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20220501"
	"k8s.io/client-go/kubernetes"
)

type Clients struct {
	Credential *common.Credential
	CVM        *cvm2017.Client
	TKE        *tke2022.Client
	Kubernetes kubernetes.Interface
}

func NewCredential(secretID string, secretKey string) *common.Credential {
	return common.NewCredential(secretID, secretKey)
}
