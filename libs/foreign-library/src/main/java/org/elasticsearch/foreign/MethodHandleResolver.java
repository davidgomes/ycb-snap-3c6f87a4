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
 * Creates the downcall {@link MethodHandle} for a resolved native symbol. Implementations can
 * customize handle creation after symbol lookup — for example adjusting the
 * {@link FunctionDescriptor} or applying {@link java.lang.invoke.MethodHandles#insertArguments}
 * based on which symbol variant was actually chosen (available via {@link ResolvedSymbol#name()}).
 *
 * <p>Implementing classes must have a public no-arg constructor.
 *
 * <p>Defaults to {@link DefaultMethodHandleResolver}, which creates a plain downcall handle for
 * the resolved address. Specify a custom implementation via
 * {@link LibrarySpecification#methodHandleResolver()}.
 */
@FunctionalInterface
public interface MethodHandleResolver {
    /**
     * Creates the downcall {@code MethodHandle} for the given resolved symbol.
     *
     * @param symbol the resolved symbol (actual name and function pointer)
     * @param descriptor the function descriptor derived from the {@link Function @Function} method
     * @param linker the native linker to create the downcall handle with
     * @param options the linker options for the downcall handle
     * @return the method handle to invoke the native function (must not be null)
     */
    MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options);
}
