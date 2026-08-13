module opl-cloud/services/fabric

go 1.25.0

require (
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs v1.3.115
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common v1.3.127
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm v1.3.127
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke v1.3.123
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc v1.3.127
	k8s.io/apimachinery v0.31.4
	opl-cloud/services/internal/postgresmigrate v0.0.0
)

require (
	github.com/agext/levenshtein v1.2.1 // indirect
	github.com/apparentlymart/go-textseg/v13 v13.0.0 // indirect
	github.com/hashicorp/hcl/v2 v2.13.0 // indirect
	github.com/mitchellh/go-wordwrap v0.0.0-20150314170334-ad45545899c7 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace opl-cloud/services/internal/postgresmigrate => ../internal/postgresmigrate

require (
	ariga.io/atlas v0.19.1-0.20240203083654-5948b60a8e43 // indirect
	entgo.io/ent v0.13.1
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-openapi/inflect v0.19.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/lib/pq v1.10.9
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/zclconf/go-cty v1.8.0 // indirect
	golang.org/x/mod v0.35.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/utils v0.0.0-20240711033017-18e509b52bc8 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.4.1 // indirect
)
