// Copyright 2026 leavesprior contributors
// SPDX-License-Identifier: Apache-2.0

package browser

// This file is intentionally minimal. The browser package uses a pluggable
// Transport interface (defined in driver.go) rather than embedding a
// concrete CDP/websocket implementation. Users supply their own Transport
// that handles the actual protocol communication.
//
// See the Transport interface in driver.go for the required contract.
