package sockd

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	pseudoRand "math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HouzuoGuo/laitos/daemon/common"
	"github.com/HouzuoGuo/laitos/daemon/dnsd"
	"github.com/HouzuoGuo/laitos/lalog"
	"github.com/HouzuoGuo/laitos/misc"
)

// WriteRand writes up to 5 packets of random data to the connection, each packet contains up to 600 bytes of data.
func WriteRand(conn net.Conn) {
	randBytesWritten := 0
	for i := 0; i < RandNum(1, 2, 5); i++ {
		randBuf := make([]byte, RandNum(80, 210, 550))
		if _, err := pseudoRand.Read(randBuf); err != nil {
			break
		}
		if err := conn.SetWriteDeadline(time.Now().Add(time.Duration(RandNum(890, 1440, 2330)) * time.Millisecond)); err != nil {
			break
		}
		if n, err := conn.Write(randBuf); err != nil && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "broken") {
			break
		} else {
			randBytesWritten += n
		}
	}
	if pseudoRand.Intn(100) < 3 {
		lalog.DefaultLogger.Info("sockd.TCP.WriteRand", conn.RemoteAddr().String(), nil, "wrote %d rand bytes", randBytesWritten)
	}
}

// TweakTCPConnection tweaks the TCP connection settings for improved responsiveness.
func TweakTCPConnection(conn *net.TCPConn) {
	_ = conn.SetNoDelay(true)
	_ = conn.SetKeepAlive(true)
	_ = conn.SetKeepAlivePeriod(60 * time.Second)
	_ = conn.SetDeadline(time.Now().Add(time.Duration(IOTimeoutSec * time.Second)))
	_ = conn.SetLinger(5)
}

/*
ReadWithRetry makes at most 5 attempts at reading incoming data from the connection.
If data is partially read before an IO error occurs, then the connection will be closed.
*/
func ReadWithRetry(conn net.Conn, buf []byte) (n int, err error) {
	attempts := 0
	for ; attempts < 5; attempts++ {
		if err = conn.SetReadDeadline(time.Now().Add(IOTimeoutSec * time.Second)); err == nil {
			if n, err = conn.Read(buf); err == nil {
				break
			} else if n > 0 {
				// IO error occurred after data is partially read, the data stream is now broken.
				_ = conn.Close()
				break
			}
		}
		// Sleep couple of seconds in between attempts
		time.Sleep(time.Second * time.Duration(attempts))
	}
	if pseudoRand.Intn(100) < 1 {
		lalog.DefaultLogger.Info("sockd.TCP.ReadWithRetry", conn.RemoteAddr().String(), err, "read %d bytes in %d attempts", n, attempts+1)
	}
	return
}

/*
WriteWithRetry makes at most 5 attempts at writing the data into the connection.
If data is partially written before an IO error occurs, then the connection will be closed.
*/
func WriteWithRetry(conn net.Conn, buf []byte) (n int, err error) {
	attempts := 0
	for ; attempts < 5; attempts++ {
		if err = conn.SetWriteDeadline(time.Now().Add(IOTimeoutSec * time.Second)); err == nil {
			if n, err = conn.Write(buf); err == nil {
				break
			} else if n > 0 {
				// IO error occurred after data is partially written, the data stream is now broken.
				_ = conn.Close()
				break
			}
		}
		// Sleep couple of seconds in between attempts
		time.Sleep(time.Second * time.Duration(attempts))
	}
	if pseudoRand.Intn(100) < 1 {
		lalog.DefaultLogger.Info("sockd.TCP.WriteWithRetry", conn.RemoteAddr().String(), err, "wrote %d bytes in %d attempts", n, attempts+1)
	}
	return
}

func PipeTCPConnection(fromConn, toConn net.Conn, doWriteRand bool) {
	defer func() {
		_ = toConn.Close()
	}()
	buf := make([]byte, MaxPacketSize)
	for {
		if misc.EmergencyLockDown {
			lalog.DefaultLogger.Warning("PipeTCPConnection", "", misc.ErrEmergencyLockDown, "")
			return
		}
		length, err := ReadWithRetry(fromConn, buf)
		if err != nil {
			if doWriteRand {
				WriteRand(fromConn)
			}
			return
		}
		if length > 0 {
			if _, err := WriteWithRetry(toConn, buf[:length]); err != nil {
				return
			}
		}
	}
}

type TCPDaemon struct {
	Address    string `json:"Address"`
	Password   string `json:"Password"`
	PerIPLimit int    `json:"PerIPLimit"`
	TCPPort    int    `json:"TCPPort"`

	DNSDaemon *dnsd.Daemon `json:"-"` // it is assumed to be already initialised

	cipher    *Cipher
	tcpServer *common.TCPServer
}

func (daemon *TCPDaemon) Initialise() error {
	daemon.cipher = &Cipher{}
	daemon.cipher.Initialise(daemon.Password)
	daemon.tcpServer = &common.TCPServer{
		ListenAddr:  daemon.Address,
		ListenPort:  daemon.TCPPort,
		AppName:     "sockd",
		App:         daemon,
		LimitPerSec: daemon.PerIPLimit,
	}
	daemon.tcpServer.Initialise()
	return nil
}

func (daemon *TCPDaemon) GetTCPStatsCollector() *misc.Stats {
	return common.SOCKDStatsTCP
}

func (daemon *TCPDaemon) HandleTCPConnection(logger lalog.Logger, ip string, client *net.TCPConn) {
	NewTCPCipherConnection(daemon, client, daemon.cipher.Copy(), logger).HandleTCPConnection()
}

func (daemon *TCPDaemon) StartAndBlock() error {
	return daemon.tcpServer.StartAndBlock()
}

func (daemon *TCPDaemon) Stop() {
	daemon.tcpServer.Stop()
}

type TCPCipherConnection struct {
	net.Conn
	*Cipher
	daemon            *TCPDaemon
	mutex             sync.Mutex
	readBuf, writeBuf []byte
	logger            lalog.Logger
}

func NewTCPCipherConnection(daemon *TCPDaemon, netConn net.Conn, cip *Cipher, logger lalog.Logger) *TCPCipherConnection {
	return &TCPCipherConnection{
		Conn:     netConn,
		daemon:   daemon,
		Cipher:   cip,
		readBuf:  make([]byte, MaxPacketSize),
		writeBuf: make([]byte, MaxPacketSize),
		logger:   logger,
	}
}

func (conn *TCPCipherConnection) Close() error {
	return conn.Conn.Close()
}

func (conn *TCPCipherConnection) Read(b []byte) (n int, err error) {
	if conn.DecryptionStream == nil {
		iv := make([]byte, conn.IVLength)
		if _, err = io.ReadFull(conn.Conn, iv); err != nil {
			return
		}
		conn.InitDecryptionStream(iv)
		if len(conn.IV) == 0 {
			conn.IV = iv
		}
	}

	cipherData := conn.readBuf
	if len(b) > len(cipherData) {
		cipherData = make([]byte, len(b))
	} else {
		cipherData = cipherData[:len(b)]
	}

	n, err = conn.Conn.Read(cipherData)
	if n > 0 {
		conn.Decrypt(b[0:n], cipherData[0:n])
	}
	return
}

func (conn *TCPCipherConnection) Write(buf []byte) (n int, err error) {
	conn.mutex.Lock()
	bufSize := len(buf)
	headerLen := len(buf) - bufSize

	var iv []byte
	if conn.EncryptionStream == nil {
		iv = conn.InitEncryptionStream()
	}

	cipherData := conn.writeBuf
	dataSize := len(buf) + len(iv)
	if dataSize > len(cipherData) {
		cipherData = make([]byte, dataSize)
	} else {
		cipherData = cipherData[:dataSize]
	}

	if iv != nil {
		copy(cipherData, iv)
	}

	conn.Encrypt(cipherData[len(iv):], buf)
	n, err = conn.Conn.Write(cipherData)

	if n >= headerLen {
		n -= headerLen
	}
	conn.mutex.Unlock()
	return
}

func (conn *TCPCipherConnection) ParseRequest() (destIP net.IP, destNoPort, destWithPort string, err error) {
	if err = conn.SetReadDeadline(time.Now().Add(IOTimeoutSec * time.Second)); err != nil {
		conn.logger.MaybeMinorError(err)
		return
	}

	buf := make([]byte, 269)
	if _, err = io.ReadFull(conn, buf[:AddressTypeIndex+1]); err != nil {
		return
	}

	var reqStart, reqEnd int
	addrType := buf[AddressTypeIndex]
	maskedType := addrType & AddressTypeMask
	switch maskedType {
	case AddressTypeIPv4:
		reqStart, reqEnd = IPPacketIndex, IPPacketIndex+IPv4PacketLength
	case AddressTypeIPv6:
		reqStart, reqEnd = IPPacketIndex, IPPacketIndex+IPv6PacketLength
	case AddressTypeDM:
		if _, err = io.ReadFull(conn, buf[AddressTypeIndex+1:DMAddrLengthIndex+1]); err != nil {
			return
		}
		reqStart, reqEnd = DMAddrIndex, DMAddrIndex+int(buf[DMAddrLengthIndex])+DMAddrHeaderLength
	default:
		err = fmt.Errorf("TCPCipherConnection.ParseRequest: unknown mask type %d", maskedType)
		return
	}

	if _, err = io.ReadFull(conn, buf[reqStart:reqEnd]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[reqEnd-2 : reqEnd])
	if port < 1 {
		err = fmt.Errorf("TCPCipherConnection.ParseRequest: invalid destination port %d", port)
		return
	}

	switch maskedType {
	case AddressTypeIPv4:
		destIP = buf[IPPacketIndex : IPPacketIndex+net.IPv4len]
		destNoPort = destIP.String()
		destWithPort = net.JoinHostPort(destIP.String(), strconv.Itoa(int(port)))
	case AddressTypeIPv6:
		destIP = buf[IPPacketIndex : IPPacketIndex+net.IPv6len]
		destNoPort = destIP.String()
		destWithPort = net.JoinHostPort(destIP.String(), strconv.Itoa(int(port)))
	case AddressTypeDM:
		dest := string(buf[DMAddrIndex : DMAddrIndex+int(buf[DMAddrLengthIndex])])
		destNoPort = dest
		destIP = net.ParseIP(dest)
		destWithPort = net.JoinHostPort(dest, strconv.Itoa(int(port)))
	}
	if strings.ContainsRune(destNoPort, 0) || strings.ContainsRune(destWithPort, 0) {
		err = fmt.Errorf("TCPCipherConnection.ParseRequest: destination must not contain NULL byte")
	}
	return
}

func (conn *TCPCipherConnection) WriteRandAndClose() {
	defer func() {
		_ = conn.Close()
	}()
	randBuf := make([]byte, RandNum(20, 70, 200))
	_, err := rand.Read(randBuf)
	if err != nil {
		conn.logger.Warning("WriteRandAndClose", conn.Conn.RemoteAddr().String(), err, "failed to get random bytes")
		return
	}
	if err := conn.SetWriteDeadline(time.Now().Add(IOTimeoutSec * time.Second)); err != nil {
		conn.logger.Warning("WriteRandAndClose", conn.Conn.RemoteAddr().String(), err, "failed to write random bytes")
		return
	}
	if _, err := conn.Write(randBuf); err != nil {
		conn.logger.Warning("WriteRandAndClose", conn.Conn.RemoteAddr().String(), err, "failed to write random bytes")
		return
	}
}

func (conn *TCPCipherConnection) HandleTCPConnection() {
	remoteAddr := conn.RemoteAddr().String()
	destIP, destNoPort, destWithPort, err := conn.ParseRequest()
	if err != nil {
		conn.logger.Warning("HandleTCPConnection", remoteAddr, err, "failed to get destination address")
		conn.WriteRandAndClose()
		return
	}
	if strings.ContainsRune(destWithPort, 0) {
		conn.logger.Warning("HandleTCPConnection", remoteAddr, nil, "will not serve invalid destination address with 0 in it")
		conn.WriteRandAndClose()
		return
	}
	if destIP != nil && IsReservedAddr(destIP) {
		conn.logger.Info("HandleTCPConnection", remoteAddr, nil, "will not serve reserved address %s", destNoPort)
		_ = conn.Close()
		return
	}
	if conn.daemon.DNSDaemon.IsInBlacklist(destNoPort) {
		conn.logger.Info("HandleTCPConnection", remoteAddr, nil, "will not serve blacklisted address %s", destNoPort)
		_ = conn.Close()
		return
	}
	dest, err := net.DialTimeout("tcp", destWithPort, IOTimeoutSec*time.Second)
	if err != nil {
		conn.logger.Warning("HandleTCPConnection", remoteAddr, err, "failed to connect to destination \"%s\"", destWithPort)
		_ = conn.Close()
		return
	}
	TweakTCPConnection(conn.Conn.(*net.TCPConn))
	TweakTCPConnection(dest.(*net.TCPConn))
	go PipeTCPConnection(conn, dest, true)
	PipeTCPConnection(dest, conn, false)
}
