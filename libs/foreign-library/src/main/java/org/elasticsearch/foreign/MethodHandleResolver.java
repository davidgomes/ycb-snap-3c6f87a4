/*
 * Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
 * or more contributor license agreements. Licensed under the "Elastic License
 * 2.0", the "GNU Affero General Public License v3.0 only", and the "Server Side
 * Public License v 1"; you may not use this file except in compliance with, at
 * your election, the "Elastic License 2.0", the "GNU Affero General Public
 * License v3.0 only", or the "Server Side Public License, v 1".
 */

package org.elasticsearch.foreign;

import java.lang.foreign.FunctionDescriptor;
import java.lang.foreign.Linker;
import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;

/**
 * Creates the downcall {@link MethodHandle} for a symbol resolved by {@link SymbolResolver}.
 * Implementations can customize {@code MethodHandle} creation after resolution — for example
 * adjusting the {@link FunctionDescriptor} or applying {@link MethodHandles#insertArguments}
 * based on {@link ResolvedSymbol#name()}, i.e. which symbol variant was actually chosen.
 *
 * <p>Implementing classes must have a public no-arg constructor. Defaults to
 * {@link DefaultMethodHandleResolver}, which simply calls
 * {@link Linker#downcallHandle(java.lang.foreign.MemorySegment, FunctionDescriptor, Linker.Option...)}
 * on the resolved address.
 *
 * <p>Example — insert a fixed leading argument for a symbol variant that requires one:
 *
 * <pre>{@code
 * public class VariantAwareResolver implements MethodHandleResolver {
 *     public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
 *         MethodHandle handle = linker.downcallHandle(symbol.address(), descriptor, options);
 *         if (symbol.name().endsWith("_v2")) {
 *             handle = MethodHandles.insertArguments(handle, 0, 2);
 *         }
 *         return handle;
 *     }
 * }
 * }</pre>
 */
@FunctionalInterface
public interface MethodHandleResolver {
    /**
     * Creates the downcall {@code MethodHandle} for a resolved native symbol.
     *
     * @param symbol the symbol resolved by {@link SymbolResolver}
     * @param descriptor the function descriptor derived from the {@link Function @Function} method signature
     * @param linker the native linker
     * @param options linker options derived from {@code @CaptureErrno}/{@code @Variadic}/{@code @Critical}
     * @return the {@code MethodHandle} used to invoke the native function (must not be null)
     */
    MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options);
}
