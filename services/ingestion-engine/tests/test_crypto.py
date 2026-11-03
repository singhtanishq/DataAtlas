"""Credential cipher tests, including the Go control-plane interop vector."""

import pytest
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from app.core.crypto import CredentialCipher, CredentialError, load_key

TEST_KEY = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

# Produced by the Go control plane (internal/crypto tests) with TEST_KEY.
GO_NONCE = bytes.fromhex("05140bc1e2d735d5c37a5576")
GO_CIPHERTEXT = bytes.fromhex(
    "dc44a9011d86c7587803cb75d77bd4474526807f2c411643adce53e9de52188a"
    "04347bcfe8ae6e00a20177c52d8ccb7cf5"
)
GO_PLAINTEXT = b'{"password":"interop-verify-123"}'


def test_decrypt_go_produced_ciphertext() -> None:
    cipher = CredentialCipher(TEST_KEY)
    assert cipher.decrypt(GO_CIPHERTEXT, GO_NONCE) == GO_PLAINTEXT


def test_decrypt_go_json_payload() -> None:
    cipher = CredentialCipher(TEST_KEY)
    assert cipher.decrypt_json(GO_CIPHERTEXT, GO_NONCE) == {"password": "interop-verify-123"}


def test_round_trip_python_encrypt_decrypt() -> None:
    key = load_key(TEST_KEY)
    nonce = b"\x01" * 12
    sealed = AESGCM(key).encrypt(nonce, b"payload", None)
    cipher = CredentialCipher(TEST_KEY)
    assert cipher.decrypt(sealed, nonce) == b"payload"


def test_tampered_ciphertext_rejected() -> None:
    cipher = CredentialCipher(TEST_KEY)
    bad = bytearray(GO_CIPHERTEXT)
    bad[0] ^= 0xFF
    with pytest.raises(CredentialError):
        cipher.decrypt(bytes(bad), GO_NONCE)


def test_wrong_key_rejected() -> None:
    other = "ff" * 32
    cipher = CredentialCipher(other)
    with pytest.raises(CredentialError):
        cipher.decrypt(GO_CIPHERTEXT, GO_NONCE)


def test_load_key_validation() -> None:
    with pytest.raises(CredentialError):
        load_key("nothex")
    with pytest.raises(CredentialError):
        load_key("a1b2")  # too short
