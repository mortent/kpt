package liveutil

import "sigs.k8s.io/cli-utils/pkg/printers"

func ValidatePrinterType(printerType string) bool {
	for _, p := range printers.SupportedPrinters() {
		if printerType == p {
			return true
		}
	}
	return false
}
