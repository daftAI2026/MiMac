#ifndef MIMAC_READY_INDICATOR_H
#define MIMAC_READY_INDICATOR_H

#include <stdint.h>

uint32_t MiMacReadyIndicatorABIVersion(void);
uintptr_t MiMacReadyIndicatorInstall(uintptr_t ns_window);
int32_t MiMacReadyIndicatorUpdate(uintptr_t handle,
                                  double x, double y, double width, double height,
                                  uint32_t rgba, int32_t animate);
void MiMacReadyIndicatorClose(uintptr_t handle);

#endif
