// Shim bindings: satu pintu masuk untuk seluruh API Go RSISA Printer Server.
// File ini hanya re-export dari bindings hasil `wails3 generate bindings`,
// sehingga perubahan path import cukup dilakukan di satu tempat.
export {
  AddOrigin,
  ClearLogs,
  ClearPrinterQueue,
  CopyServerAddress,
  DiagnosePort,
  DiscoverNetworkPrinters,
  ExportConfig,
  GetAutostart,
  GetConfig,
  GetLogs,
  GetOrigins,
  GetPrinterCount,
  GetPrinterPorts,
  GetPrinters,
  GetSettings,
  GetState,
  HidePopup,
  ImportConfig,
  InstallNetworkPrinter,
  RemoveNetworkPrinter,
  OpenConfigFolder,
  OpenServerURL,
  QuitApp,
  ReloadConfig,
  RemoveOrigin,
  ResetConfig,
  RestartService,
  SaveSettings,
  SetAutostart,
  SetListen,
  SetPrinterPort,
  ShowPanel,
  StartService,
  StopService,
  TestConnection,
  TestPrinter,
  ToggleService,
  UninstallApp,
  UpdateOrigin,
} from "../bindings/github.com/RSISA-Semarang/rsisa-print-server/desktop/aquilaservice.js";

export {
  AquilaLog,
  AquilaState,
} from "../bindings/events/index.js";

import { Call as $Call } from "@wailsio/runtime";

export function SetCloudAgent(enabled, serverURL, pollInterval, restart) {
  return $Call.ByName("main.AquilaService.SetCloudAgent", enabled, serverURL, pollInterval, restart);
}

export function RegisterPrintersToCloud() {
  return $Call.ByName("main.AquilaService.RegisterPrintersToCloud");
}
