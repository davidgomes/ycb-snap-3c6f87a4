/*
 * Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
 * or more contributor license agreements. Licensed under the "Elastic License
 * 2.0", the "GNU Affero General Public License v3.0 only", and the "Server Side
 * Public License v 1"; you may not use this file except in compliance with, at
 * your election, the "Elastic License 2.0", the "GNU Affero General Public
 * License v3.0 only", or the "Server Side Public License, v 1".
 */

package org.elasticsearch.foreign.processor;

import org.elasticsearch.core.SuppressForbidden;

import java.lang.invoke.MethodHandle;

/**
 * Tests for the {@code methodHandleResolver} parameter on {@code @LibrarySpecification}.
 */
@SuppressForbidden(reason = "tests verify private/static fields of processor-generated classes and their support classes")
public class MethodHandleResolverClassTests extends ProcessorTestCase {

    /**
     * A valid resolver implementing MethodHandleResolver with a no-arg constructor compiles cleanly.
     */
    public void testValidMethodHandleResolverCompiles() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.foreign.MemorySegment;
            import java.lang.foreign.SymbolLookup;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            class FakeSymbolResolver implements SymbolResolver {
                public FakeSymbolResolver() {}
                public ResolvedSymbol resolve(String name, SymbolLookup lookup) {
                    return new ResolvedSymbol(name, MemorySegment.ofAddress(1L));
                }
            }
            class MyMhResolver implements MethodHandleResolver {
                public MyMhResolver() {}
                @Override
                public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            @LibrarySpecification(symbolResolver = FakeSymbolResolver.class, methodHandleResolver = MyMhResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        Class<?> implClass = result.loadClass("test.MyLib$Impl");
        assertNotNull("Generated MyLib$Impl class not found", implClass);

        java.lang.reflect.Field mhField = implClass.getDeclaredField("add$mh");
        assertEquals("add$mh must be a MethodHandle", MethodHandle.class, mhField.getType());
    }

    /**
     * The methodHandleResolver class must implement MethodHandleResolver. The type bound on the
     * annotation parameter ({@code Class<? extends MethodHandleResolver>}) causes javac to reject a
     * class that doesn't implement the interface before the processor even runs.
     */
    public void testMethodHandleResolverNotImplementingInterfaceEmitsError() {
        String source = """
            package test;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            class BadMhResolver {
                public BadMhResolver() {}
            }
            @LibrarySpecification(name = "testlib", methodHandleResolver = BadMhResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertFalse("Expected compilation to fail when resolver doesn't implement MethodHandleResolver", result.success());
        boolean hasError = result.errors().stream().anyMatch(msg -> msg.contains("cannot be converted to"));
        assertTrue("Expected type mismatch error but got: " + result.errors(), hasError);
    }

    /**
     * The methodHandleResolver class must have a public no-arg constructor.
     */
    public void testMethodHandleResolverMissingNoArgConstructorEmitsError() {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            class BadMhResolver implements MethodHandleResolver {
                public BadMhResolver(String config) {}
                @Override
                public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            @LibrarySpecification(name = "testlib", methodHandleResolver = BadMhResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertFalse("Expected compilation to fail when resolver has no no-arg constructor", result.success());
        boolean hasError = result.errors().stream().anyMatch(msg -> msg.contains("must have a public no-arg constructor"));
        assertTrue("Expected error about no-arg constructor but got: " + result.errors(), hasError);
    }

    /**
     * Without a custom methodHandleResolver, the generated code uses DefaultMethodHandleResolver.
     */
    public void testNoMethodHandleResolverUsesDefault() throws Exception {
        String source = """
            package test;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            @LibrarySpecification(name = "testlib")
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());
        assertNotNull(result.loadClassNoInit("test.MyLib$Impl"));
    }

    /**
     * A custom {@code methodHandleResolver} must actually be invoked at class-init time, and it
     * must observe the symbol name chosen by a custom {@code symbolResolver} (not the raw
     * {@code @Function} name) — proving the resolved name flows from symbol resolution through to
     * method handle creation.
     */
    public void testCustomMethodHandleResolverSeesResolvedSymbolName() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.foreign.MemorySegment;
            import java.lang.foreign.SymbolLookup;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            @LibrarySpecification(symbolResolver = RecordingLib.PrefixResolver.class, methodHandleResolver = RecordingLib.RecordingMhResolver.class)
            public interface RecordingLib {
                @Function("compress")
                int compress(int a, int b);

                class PrefixResolver implements SymbolResolver {
                    public PrefixResolver() {}
                    public ResolvedSymbol resolve(String name, SymbolLookup lookup) {
                        return new ResolvedSymbol("mylib_" + name, MemorySegment.ofAddress(1L));
                    }
                }

                class RecordingMhResolver implements MethodHandleResolver {
                    public static String lastResolvedName;

                    public RecordingMhResolver() {}

                    @Override
                    public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
                        lastResolvedName = symbol.name();
                        return linker.downcallHandle(symbol.address(), descriptor, options);
                    }
                }
            }
            """;

        CompilationResult result = compile("test.RecordingLib", source);
        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        // Loading with init runs <clinit>, which must invoke RecordingMhResolver.resolve(...).
        Class<?> implClass = result.loadClass("test.RecordingLib$Impl");
        assertNotNull("Generated RecordingLib$Impl class not found", implClass);

        // Resolve the resolver class through $Impl's own class loader so we read back the same
        // class (and static field) that <clinit> actually initialized and wrote to.
        Class<?> resolverClass = Class.forName("test.RecordingLib$RecordingMhResolver", false, implClass.getClassLoader());
        String lastResolvedName = (String) resolverClass.getField("lastResolvedName").get(null);
        assertEquals("methodHandleResolver must observe the name chosen by symbolResolver", "mylib_compress", lastResolvedName);
    }
}
