package workspacebackup

import (
	"bytes"
	"errors"
	"io"

	"filippo.io/age"
)

var (
	ErrAuthentication = errors.New("workspace backup authentication failed")
	ErrInvalidBundle  = errors.New("invalid workspace backup")
	ErrPassphrase     = errors.New("valid workspace backup passphrase is required")
)

var envelopeMagic = []byte("MLINK-BACKUP\n")

const (
	minimumPassphraseBytes = 12
	maximumPassphraseBytes = 4096
	scryptWorkFactor       = 18
)

func Encrypt(destination io.Writer, passphrase []byte, source io.Reader) error {
	if destination == nil || source == nil {
		return errors.New("workspace backup source and destination are required")
	}
	if err := validatePassphrase(passphrase); err != nil {
		return err
	}
	recipient, err := age.NewScryptRecipient(string(passphrase))
	if err != nil {
		return ErrPassphrase
	}
	recipient.SetWorkFactor(scryptWorkFactor)
	if _, err := destination.Write(envelopeMagic); err != nil {
		return errors.New("write workspace backup header")
	}
	encrypted, err := age.Encrypt(destination, recipient)
	if err != nil {
		return errors.New("initialize workspace backup encryption")
	}
	if _, err := io.Copy(encrypted, source); err != nil {
		_ = encrypted.Close()
		return errors.New("encrypt workspace backup")
	}
	if err := encrypted.Close(); err != nil {
		return errors.New("finalize workspace backup encryption")
	}
	return nil
}

func Decrypt(destination io.Writer, passphrase []byte, source io.Reader) error {
	if destination == nil || source == nil {
		return errors.New("workspace backup source and destination are required")
	}
	if err := validatePassphrase(passphrase); err != nil {
		return err
	}
	clear, err := decryptReader(passphrase, source)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, clear); err != nil {
		return ErrAuthentication
	}
	return nil
}

func decryptReader(passphrase []byte, source io.Reader) (io.Reader, error) {
	if source == nil {
		return nil, errors.New("workspace backup source is required")
	}
	if err := validatePassphrase(passphrase); err != nil {
		return nil, err
	}
	if _, err := consumeHeader(source); err != nil {
		return nil, err
	}
	identity, err := age.NewScryptIdentity(string(passphrase))
	if err != nil {
		return nil, ErrAuthentication
	}
	clear, err := age.Decrypt(source, identity)
	if err != nil {
		return nil, ErrAuthentication
	}
	return clear, nil
}

func InspectHeader(source io.Reader) (Header, error) {
	if source == nil {
		return Header{}, ErrInvalidBundle
	}
	return consumeHeader(source)
}

func consumeHeader(source io.Reader) (Header, error) {
	header := make([]byte, len(envelopeMagic))
	if _, err := io.ReadFull(source, header); err != nil || !bytes.Equal(header, envelopeMagic) {
		return Header{}, ErrInvalidBundle
	}
	return Header{Format: FormatV1}, nil
}

func validatePassphrase(passphrase []byte) error {
	if len(passphrase) < minimumPassphraseBytes || len(passphrase) > maximumPassphraseBytes {
		return ErrPassphrase
	}
	return nil
}
