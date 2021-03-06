package balancer

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onestraw/golb/config"
)

var (
	VSAddr = "127.0.0.1:8083"
	S1     string
	S2     string
)

func TestVirtualServer(t *testing.T) {
	s1 := httptest.NewServer(newHandler("s1"))
	s2 := httptest.NewServer(newHandler("s2"))
	S1, S2 = s1.URL[7:], s2.URL[7:]
	if S1 > S2 {
		S1, S2 = S2, S1
	}
	jsonBody := fmt.Sprintf(`{"virtual_server":[{"name":"web","address":"%s","pool":[{"address":"%s","weight":1},{"address":"%s","weight":1}]}]}`, VSAddr, S1, S2)

	c, err := config.LoadFromString(jsonBody)
	require.NoError(t, err)

	cvs := c.VServers[0]
	vs, err := NewVirtualServer(
		NameOpt(cvs.Name),
		AddressOpt(cvs.Address),
		PoolOpt(cvs.Pool),
	)
	require.NoError(t, err)

	// test run
	require.NoError(t, vs.Run())
	time.Sleep(time.Second)
	// test repeated run
	err = vs.Run()
	assert.Contains(t, err.Error(), "already enabled")

	// test LB
	result := map[string]int{}
	for i := 0; i < 10; i += 1 {
		resp, err := request(VSAddr)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		result[resp.Body] += 1
	}
	assert.Equal(t, 5, result["s1"])
	assert.Equal(t, 5, result["s2"])

	// test stats
	expectStats := fmt.Sprintf("Pool-web\n%s\nstatus_code: 200:5\nmethod: GET:5\npath: /:5\nrecv_bytes: 0\nsend_bytes: 10\n------\n%s\nstatus_code: 200:5\nmethod: GET:5\npath: /:5\nrecv_bytes: 0\nsend_bytes: 10\n------", S1, S2)
	assert.Equal(t, expectStats, vs.Stats())

	// test pool
	assert.Equal(t, 2, vs.Pool.Size())

	peer := "127.0.0.1:10009"
	vs.AddPeer(peer)
	assert.Equal(t, 3, vs.Pool.Size())

	vs.AddPeer(peer)
	assert.Equal(t, 3, vs.Pool.Size())

	vs.RemovePeer(peer)
	assert.Equal(t, 2, vs.Pool.Size())

	vs.RemovePeer(peer)
	assert.Equal(t, 2, vs.Pool.Size())

	// test stop
	require.NoError(t, vs.Stop())
	assert.Equal(t, STATUS_DISABLED, vs.Status())
	// test repeated stop
	err = vs.Stop()
	assert.Contains(t, err.Error(), "already disabled")
}

func TestVirtualServerFail(t *testing.T) {
	addr := "127.0.0.1:8084"
	jsonBody := fmt.Sprintf(`{"virtual_server":[{"name":"web","address":"%s","pool":[{"address":"127.0.0.1:12345","weight":1}]}]}`, addr)

	c, err := config.LoadFromString(jsonBody)
	require.NoError(t, err)

	cvs := c.VServers[0]
	vs, err := NewVirtualServer(
		NameOpt(cvs.Name),
		AddressOpt(cvs.Address),
		PoolOpt(cvs.Pool),
	)
	require.NoError(t, err)
	require.NoError(t, vs.Run())
	time.Sleep(time.Second)

	// test maxfails
	for i := 0; i < DEFAULT_MAXFAILS; i++ {
		resp, err := request(addr)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	}
	resp, err := request(addr)
	require.NoError(t, err)
	assert.Equal(t, ErrPeerNotFound.StatusCode, resp.StatusCode)
	assert.Equal(t, ErrPeerNotFound.ErrMsg, resp.Body)

	// test fail recovery
	vs.FailTimeout = 1
	time.Sleep(time.Second)
	resp, err = request(addr)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.NotEqual(t, ErrPeerNotFound.ErrMsg, resp.Body)

	require.NoError(t, vs.Stop())
	assert.Equal(t, STATUS_DISABLED, vs.Status())
}

func TestVirtualServerError(t *testing.T) {
	addr := "127.0.0.1:8085"
	vs, err := NewVirtualServer(
		NameOpt("web"),
		AddressOpt(addr),
		PoolOpt([]config.Server{}),
	)
	require.NoError(t, err)
	require.NoError(t, vs.Run())
	time.Sleep(time.Second)

	resp, err := request(addr)
	require.NoError(t, err)
	assert.Equal(t, ErrPeerNotFound.StatusCode, resp.StatusCode)
	assert.Equal(t, ErrPeerNotFound.ErrMsg, resp.Body)

	vs.ServerName = addr
	resp, err = request(addr)
	require.NoError(t, err)
	assert.Equal(t, ErrHostNotMatch.StatusCode, resp.StatusCode)
	assert.Equal(t, ErrHostNotMatch.ErrMsg, resp.Body)

	require.NoError(t, vs.Stop())
	assert.Equal(t, STATUS_DISABLED, vs.Status())
}

func TestOpt(t *testing.T) {
	vs, err := NewVirtualServer()
	assert.Nil(t, vs)
	assert.Equal(t, ErrVirtualServerNameEmpty, err)

	vs, err = NewVirtualServer(NameOpt("web"))
	assert.Nil(t, vs)
	assert.Equal(t, ErrVirtualServerAddressEmpty, err)

	vs, err = NewVirtualServer(NameOpt(""))
	assert.Nil(t, vs)
	assert.Equal(t, ErrVirtualServerNameEmpty, err)

	vs, err = NewVirtualServer(AddressOpt(""))
	assert.Nil(t, vs)
	assert.Equal(t, ErrVirtualServerAddressEmpty, err)

	vs, err = NewVirtualServer(NameOpt("web"), AddressOpt(":80"), ServerNameOpt(""))
	require.NoError(t, err)
	assert.Equal(t, DEFAULT_SERVERNAME, vs.ServerName)

	vs, err = NewVirtualServer(NameOpt("web"), AddressOpt(":80"), ProtocolOpt(""))
	require.NoError(t, err)
	assert.Equal(t, PROTO_HTTP, vs.Protocol)

	vs, err = NewVirtualServer(ProtocolOpt("grpc"))
	require.Nil(t, vs)
	assert.Equal(t, ErrNotSupportedProto, err)

	vs, err = NewVirtualServer(ProtocolOpt("https"), TLSOpt("", ""))
	assert.Nil(t, vs)
	assert.Contains(t, err.Error(), "not exist")

	cert, err := ioutil.TempFile("", "temp.pem")
	key, err := ioutil.TempFile("", "temp.key")
	require.NoError(t, err)
	defer syscall.Unlink(cert.Name())

	vs, err = NewVirtualServer(ProtocolOpt("https"), TLSOpt(cert.Name(), ""))
	assert.Nil(t, vs)
	assert.Contains(t, err.Error(), "not exist")

	vs, err = NewVirtualServer(NameOpt("web"), AddressOpt(":80"), ProtocolOpt("https"), TLSOpt(cert.Name(), key.Name()))
	assert.Nil(t, err)
	assert.NotNil(t, vs)

	vs, err = NewVirtualServer(NameOpt("web"), AddressOpt(":80"), LBMethodOpt(""))
	require.NoError(t, err)
	assert.Equal(t, LB_ROUNDROBIN, vs.LBMethod)

	vs, err = NewVirtualServer(LBMethodOpt("hash"))
	assert.Nil(t, vs)
	assert.Equal(t, err, ErrNotSupportedMethod)

	vs, err = NewVirtualServer(NameOpt("web"), AddressOpt(":80"), RetryOpt(true))
	require.NoError(t, err)
	assert.Equal(t, true, vs.retry)
}
