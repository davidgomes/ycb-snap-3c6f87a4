/*
 * Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
 * or more contributor license agreements. Licensed under the "Elastic License
 * 2.0", the "GNU Affero General Public License v3.0 only", and the "Server Side
 * Public License v 1"; you may not use this file except in compliance with, at
 * your election, the "Elastic License 2.0", the "GNU Affero General Public
 * License v3.0 only", or the "Server Side Public License, v 1".
 */

package org.elasticsearch.foreign;

import junit.framework.TestCase;

import java.lang.foreign.FunctionDescriptor;
import java.lang.foreign.Linker;
import java.lang.foreign.MemorySegment;
import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodType;

import static java.lang.foreign.ValueLayout.JAVA_INT;

/**
 * Tests that the default resolver preserves the original downcall behavior.
 */
public class DefaultMethodHandleResolverTests extends TestCase {

    public void testResolveDelegatesToLinkerDowncallHandle() {
        MemorySegment address = MemorySegment.ofAddress(1L);
        ResolvedSymbol symbol = new ResolvedSymbol("native_add", address);
        FunctionDescriptor descriptor = FunctionDescriptor.of(JAVA_INT, JAVA_INT, JAVA_INT);
        Linker linker = Linker.nativeLinker();

        MethodHandle viaResolver = new DefaultMethodHandleResolver().resolve(symbol, descriptor, linker);
        MethodHandle viaLinker = linker.downcallHandle(address, descriptor);

        assertNotNull(viaResolver);
        assertEquals(viaLinker.type(), viaResolver.type());
        assertEquals(MethodType.methodType(int.class, int.class, int.class), viaResolver.type());
    }
}
