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

import java.lang.foreign.MemorySegment;
import java.lang.foreign.SymbolLookup;
import java.util.Optional;

/**
 * Tests the default resolver's exact-name lookup behavior and richer result.
 */
public class DefaultSymbolResolverTests extends TestCase {

    public void testResolveReturnsRequestedNameAndAddress() {
        MemorySegment address = MemorySegment.ofAddress(42L);
        SymbolLookup lookup = name -> name.equals("foo") ? Optional.of(address) : Optional.empty();

        ResolvedSymbol resolved = new DefaultSymbolResolver().resolve("foo", lookup);

        assertEquals("foo", resolved.name());
        assertEquals(address, resolved.address());
    }

    public void testResolveThrowsWhenSymbolIsMissing() {
        SymbolLookup lookup = name -> Optional.empty();

        try {
            new DefaultSymbolResolver().resolve("missing", lookup);
            fail("expected UnsatisfiedLinkError");
        } catch (UnsatisfiedLinkError e) {
            assertTrue(e.getMessage().contains("missing"));
        }
    }
}
