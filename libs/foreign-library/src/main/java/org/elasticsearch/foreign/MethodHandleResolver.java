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

/**
 * Creates a downcall {@link MethodHandle} from a {@link ResolvedSymbol}. Implementations can
 * adjust the function descriptor or apply {@link java.lang.invoke.MethodHandles} combinators
 * based on which symbol variant was actually chosen.
 *
 * <p>The resolver receives the symbol resolved by {@link SymbolResolver}, the function descriptor
 * derived from the Java method signature, the native {@link Linker}, and any {@link Linker.Option}
 * values implied by annotations such as {@link CaptureErrno} or {@link Variadic}. The default
 * implementation ({@link DefaultMethodHandleResolver}) calls
 * {@link Linker#downcallHandle(java.lang.foreign.MemorySegment, FunctionDescriptor, Linker.Option...)}
 * on the resolved address.
 *
 * <p>Implementing classes must have a public no-arg constructor.
 *
 * <p>Example — a resolver that binds a trailing capability-level argument when a suffixed symbol
 * was selected:
 *
 * <pre>{@code
 * public class CapabilityMethodHandleResolver implements MethodHandleResolver {
 *     @Override
 *     public MethodHandle resolve(
 *         ResolvedSymbol symbol,
 *         FunctionDescriptor descriptor,
 *         Linker linker,
 *         Linker.Option... options
 *     ) {
 *         MethodHandle mh = linker.downcallHandle(symbol.address(), descriptor, options);
 *         int sep = symbol.name().lastIndexOf('_');
 *         if (sep < 0) {
 *             return mh;
 *         }
 *         int level = Integer.parseInt(symbol.name().substring(sep + 1));
 *         return MethodHandles.insertArguments(mh, mh.type().parameterCount() - 1, level);
 *     }
 * }
 * }</pre>
 */
@FunctionalInterface
public interface MethodHandleResolver {
    /**
     * Creates a downcall method handle for the resolved native symbol.
     *
     * @param symbol the resolved symbol (chosen name and address)
     * @param descriptor the function descriptor derived from the Java method signature
     * @param linker the native linker
     * @param options linker options such as capture-call-state or first-variadic-arg
     * @return the downcall method handle
     */
    MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options);
}
