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
 * Creates a downcall method handle for a {@link ResolvedSymbol}.
 *
 * <p>Implementations can adjust the function descriptor or bind arguments according to the
 * native symbol name chosen by a {@link SymbolResolver}.
 *
 * <p>Implementing classes must have a public no-arg constructor.
 */
@FunctionalInterface
public interface MethodHandleResolver {
    /**
     * Creates the method handle used to invoke a resolved native symbol.
     *
     * @param symbol the native symbol selected by the symbol resolver
     * @param descriptor the generated function descriptor
     * @param linker the native linker
     * @param options the generated linker options
     * @return the method handle used by the generated library implementation
     */
    MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options);
}
