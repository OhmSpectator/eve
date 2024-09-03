// Copyright (c) 2024 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

#ifndef MEMORY_MONITOR_PSI_H
#define MEMORY_MONITOR_PSI_H

#include <stdbool.h>

bool psi_is_enabled();

void* psi_monitor_thread(void *args);

#endif //MEMORY_MONITOR_PSI_H
