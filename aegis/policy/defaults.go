package policy

var (
	// PlatformDesktopLinux represents standard desktop Linux (e.g., Ubuntu, Fedora, Debian)
	// without specialized confidential computing hardware.
	PlatformDesktopLinux = PlatformCapabilities{
		Name:                       "linux-desktop",
		SupportsHardenedLocal:      true,
		SupportsIsolatedLocal:      false, // requires confidential computing setup
		SupportsRemoteConfidential: true,
		SupportsAttestation:        false,
		Limitations: []string{
			"Root can inspect ordinary processes",
			"No hardware isolation without confidential computing setup",
		},
	}

	// PlatformAndroid represents the Android mobile operating system.
	PlatformAndroid = PlatformCapabilities{
		Name:                       "android",
		SupportsHardenedLocal:      true,
		SupportsIsolatedLocal:      false,
		SupportsRemoteConfidential: true,
		SupportsAttestation:        true,
		Limitations: []string{
			"Rooted devices can inspect ordinary processes",
			"Hardware isolation limited to TEE-backed keystore operations",
		},
	}

	// PlatformIOS represents the Apple iOS mobile operating system.
	PlatformIOS = PlatformCapabilities{
		Name:                       "ios",
		SupportsHardenedLocal:      true,
		SupportsIsolatedLocal:      false,
		SupportsRemoteConfidential: true,
		SupportsAttestation:        true,
		Limitations: []string{
			"Jailbroken devices compromise sandbox guarantees",
			"Hardware isolation limited to Secure Enclave key management",
		},
	}

	// PlatformWeb represents WebAssembly in web browser runtimes.
	PlatformWeb = PlatformCapabilities{
		Name:                       "web",
		SupportsHardenedLocal:      false,
		SupportsIsolatedLocal:      false,
		SupportsRemoteConfidential: true,
		SupportsAttestation:        false,
		Limitations: []string{
			"Browser environment exposed to DOM inspection and extensions",
			"No hardware isolation or local process hardening",
		},
	}

	// PlatformDesktopMacOS represents macOS desktop and server environments.
	PlatformDesktopMacOS = PlatformCapabilities{
		Name:                       "macos-desktop",
		SupportsHardenedLocal:      true,
		SupportsIsolatedLocal:      false,
		SupportsRemoteConfidential: true,
		SupportsAttestation:        true,
		Limitations: []string{
			"Root and debugger entitlements can inspect process memory",
			"Apple Silicon Secure Enclave does not support general-purpose isolated execution",
		},
	}

	// PlatformDesktopWindows represents Microsoft Windows desktop and server systems.
	PlatformDesktopWindows = PlatformCapabilities{
		Name:                       "windows-desktop",
		SupportsHardenedLocal:      true,
		SupportsIsolatedLocal:      false,
		SupportsRemoteConfidential: true,
		SupportsAttestation:        true,
		Limitations: []string{
			"Administrator accounts and kernel drivers can inspect user-mode processes",
			"VBS enclaves require specific enterprise configurations",
		},
	}

	// PlatformConfidentialLinux represents Linux running on hardware with AMD SEV-SNP or Intel SGX/TDX enabled.
	PlatformConfidentialLinux = PlatformCapabilities{
		Name:                       "linux-confidential",
		SupportsHardenedLocal:      true,
		SupportsIsolatedLocal:      true, // Linux with SEV-SNP/SGX
		SupportsRemoteConfidential: true,
		SupportsAttestation:        true,
		Limitations: []string{
			"Hypervisor controls physical resource scheduling and timing",
			"Firmware and microcode updates require platform provider verification",
		},
	}
)

// WellKnownPlatforms returns a copy of all predefined platform capability configurations.
func WellKnownPlatforms() map[string]PlatformCapabilities {
	return map[string]PlatformCapabilities{
		PlatformDesktopLinux.Name:      PlatformDesktopLinux,
		PlatformAndroid.Name:           PlatformAndroid,
		PlatformIOS.Name:               PlatformIOS,
		PlatformWeb.Name:               PlatformWeb,
		PlatformDesktopMacOS.Name:      PlatformDesktopMacOS,
		PlatformDesktopWindows.Name:    PlatformDesktopWindows,
		PlatformConfidentialLinux.Name: PlatformConfidentialLinux,
	}
}

// LookupPlatform returns predefined capabilities for a platform name, if known.
func LookupPlatform(name string) (PlatformCapabilities, bool) {
	p, ok := WellKnownPlatforms()[name]
	return p, ok
}
