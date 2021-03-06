import React from 'react';
import { StatsPicker } from '../StatsPicker/StatsPicker';
import { ReduceTransformerOptions, DataTransformerID, ReducerID } from '@grafana/data';
import { TransformerUIRegistyItem, TransformerUIProps } from './types';
import { dataTransformers } from '@grafana/data';

// TODO:  Minimal implementation, needs some <3
export const ReduceTransformerEditor: React.FC<TransformerUIProps<ReduceTransformerOptions>> = ({
  options,
  onChange,
  input,
}) => {
  return (
    <StatsPicker
      width={12}
      placeholder="Choose Stat"
      allowMultiple
      stats={options.reducers || []}
      onChange={stats => {
        onChange({
          ...options,
          reducers: stats as ReducerID[],
        });
      }}
    />
  );
};

export const reduceTransformRegistryItem: TransformerUIRegistyItem<ReduceTransformerOptions> = {
  id: DataTransformerID.reduce,
  component: ReduceTransformerEditor,
  transformer: dataTransformers.get(DataTransformerID.reduce),
  name: 'Reduce',
  description: 'UI for reduce transformation',
};
