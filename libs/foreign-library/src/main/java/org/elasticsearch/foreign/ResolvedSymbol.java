/*
 * Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
 * or more contributor license agreements. Licensed under the "Elastic License
 * 2.0", the "GNU Affero General Public License v3.0 only", and the "Server Side
 * Public License v 1"; you may not use this file except in compliance with, at
 * your election, the "Elastic License 2.0", the "GNU Affero General Public
 * License v3.0 only", or the "Server Side Public License, v 1".
 */

package org.elasticsearch.foreign;

import java.lang.foreign.MemorySegment;

/**
 * The result of {@link SymbolResolver#resolve}: the native symbol name that was actually chosen
 * and its function-pointer address.
 *
 * <p>Returning the chosen name alongside the address lets a {@link MethodHandleResolver} customize
 * {@link java.lang.invoke.MethodHandle} creation based on which symbol variant was selected — for
 * example adjusting the {@link java.lang.foreign.FunctionDescriptor} or applying
 * {@link java.lang.invoke.MethodHandles#insertArguments}.
 *
 * @param name the native symbol name that was resolved (may differ from the name requested in
 *        {@link Function @Function} when the resolver applies mangling or fallback)
 * @param address the function pointer for {@code name}
 */
public record ResolvedSymbol(String name, MemorySegment address) {}
