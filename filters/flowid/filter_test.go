package flowid

import (
	"github.com/zalando/skipper/filters"
	"github.com/zalando/skipper/mock"
	"github.com/zalando/skipper/skipper"
	"net/http"
	"testing"
)

const (
	testFlowId    = "FLOW-ID-FOR-TESTING"
	invalidFlowId = "[<>] (o) [<>]"
)

var (
	testFlowIdSpec           = &flowId{}
	filterConfigWithReuse    = skipper.FilterConfig{true}
	filterConfigWithoutReuse = skipper.FilterConfig{false}
)

func TestNewFlowIdGeneration(t *testing.T) {
	f, _ := testFlowIdSpec.MakeFilter(filterName, filterConfigWithReuse)
	fc := buildfilterContext()
	f.Request(fc)

	flowId := fc.Request().Header.Get(flowIdHeaderName)
	if !isValid(flowId) {
		t.Errorf("'%s' is not a valid flow id", flowId)
	}
}

func TestFlowIdReuseExisting(t *testing.T) {
	f, _ := testFlowIdSpec.MakeFilter(filterName, filterConfigWithReuse)
	fc := buildfilterContext(flowIdHeaderName, testFlowId)
	f.Request(fc)

	flowId := fc.Request().Header.Get(flowIdHeaderName)
	if flowId != testFlowId {
		t.Errorf("Got wrong flow id. Expected '%s' got '%s'", testFlowId, flowId)
	}
}

func TestFlowIdIgnoreReuseExisting(t *testing.T) {
	f, _ := testFlowIdSpec.MakeFilter(filterName, filterConfigWithoutReuse)
	fc := buildfilterContext(flowIdHeaderName, testFlowId)
	f.Request(fc)

	flowId := fc.Request().Header.Get(flowIdHeaderName)
	if flowId == testFlowId {
		t.Errorf("Got wrong flow id. Expected a newly generated flowid but got the test flow id '%s'", flowId)
	}
}

func TestFlowIdRejectInvalidReusedFlowId(t *testing.T) {
	f, _ := testFlowIdSpec.MakeFilter(filterName, filterConfigWithReuse)
	fc := buildfilterContext(flowIdHeaderName, invalidFlowId)
	f.Request(fc)

	flowId := fc.Request().Header.Get(flowIdHeaderName)
	if flowId == invalidFlowId {
		t.Errorf("Got wrong flow id. Expected a newly generated flowid but got the test flow id '%s'", flowId)
	}
}

func TestFlowIdWithSpecificLen(t *testing.T) {
	fc := skipper.FilterConfig{true, float64(42.0)}
	f, _ := testFlowIdSpec.MakeFilter(filterName, fc)
	fctx := buildfilterContext()
	f.Request(fctx)

	flowId := fctx.Request().Header.Get(flowIdHeaderName)

	l := len(flowId)
	if l != 42 {
		t.Errorf("Wrong flowId len. Expected %d, got %d", 42, l)
	}
}

func TestFlowIdWithInvalidParameters(t *testing.T) {
	fc := skipper.FilterConfig{"wrong-parameter-type"}
	_, err := testFlowIdSpec.MakeFilter(filterName, fc)
	if err != filters.ErrInvalidFilterParameters {
		t.Errorf("Expected an invalid parameters error, got %s", err)
	}

	fc = skipper.FilterConfig{true, float64(minLength - 1)}
	_, err = testFlowIdSpec.MakeFilter(filterName, fc)
	if err != filters.ErrInvalidFilterParameters {
		t.Errorf("Expected an invalid parameters error, got %s", err)
	}
}

func buildfilterContext(headers ...string) skipper.FilterContext {
	r, _ := http.NewRequest("GET", "http://example.org", nil)
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return &mock.FilterContext{FRequest: r}
}
